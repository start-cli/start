package cli

import (
	"context"
	"fmt"
	"time"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/cache"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/doctor"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/spf13/cobra"
)

func addDoctorCommand(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:     "doctor",
		GroupID: "utilities",
		Short:   "Diagnose start installation and configuration",
		Long: `Check start installation, configuration, and environment for issues.
Reports warnings and suggestions for any problems found.

Checks performed:
  - Version and build information
  - Configuration file validation (CUE syntax)
  - Schema validation (fetched from registry)
  - Agent binary availability
  - Context and role file existence
  - Skills inventory dest health and SKILL.md frontmatter
  - Environment (directory permissions)

Exit codes:
  0 - All checks passed
  1 - Issues found`,
		Args: noArgsOrHelp,
		RunE: runDoctor,
	}

	cmd.Flags().Bool("json", false, "Output as JSON")

	addDoctorValidateCommand(cmd)

	parent.AddCommand(cmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	report, err := prepareDoctor(cmd, getProvider(cmd), reservedCommandNames(cmd.Root()))
	if err != nil {
		return err
	}

	jsonFlag, _ := cmd.Flags().GetBool("json")
	if jsonFlag {
		if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
			return fmt.Errorf("marshalling doctor report: %w", err)
		}
		if report.HasIssues() {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			return errDoctorIssuesFound
		}
		return nil
	}

	flags := getFlags(cmd)
	reporter := doctor.NewReporter(cmd.OutOrStdout(), flags.Verbose, flags.Quiet)
	reporter.Print(report)

	if report.HasIssues() {
		// Silent error sets exit code 1.
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return errDoctorIssuesFound
	}

	return nil
}

// errDoctorIssuesFound is returned when doctor finds issues. It implements
// SilentError so main.go skips printing it.
var errDoctorIssuesFound = &doctorError{}

type doctorError struct{}

func (e *doctorError) Error() string { return "issues found" }
func (e *doctorError) Silent() bool  { return true }

// prepareDoctor runs all checks and builds the report. The provider supplies the
// registry client so tests can run doctor offline against a stub. reserved is the
// live command-name set, so the alias check can flag a name a command shadows.
func prepareDoctor(cmd *cobra.Command, provider clientProvider, reserved map[string]bool) (doctor.Report, error) {
	var report doctor.Report

	report.Sections = append(report.Sections, doctor.CheckIntro())

	indexPath := resolveLibraryIndexPath()
	buildInfo := doctor.BuildInfo{
		Version:      cliVersion,
		Commit:       commit,
		BuildDate:    buildDate,
		GoVersion:    doctor.DefaultBuildInfo().GoVersion,
		Platform:     doctor.DefaultBuildInfo().Platform,
		IndexVersion: resolveIndexVersion(indexPath, provider),
		IndexPath:    indexPath,
	}
	report.Sections = append(report.Sections, doctor.CheckVersion(buildInfo))

	report.Sections = append(report.Sections, doctor.CheckCache())

	paths, err := config.ResolvePaths("")
	if err != nil {
		return report, err
	}
	report.Sections = append(report.Sections, doctor.CheckConfiguration(paths))

	report.Sections = append(report.Sections, fetchAndValidateSchemas(paths, provider))

	var cfgLoaded bool
	var cfgResult internalcue.LoadResult

	if paths.AnyExists() {
		loader := internalcue.NewLoader()
		dirs := paths.ForScope(config.ScopeMerged)
		if len(dirs) > 0 {
			cfgResult, err = loader.Load(dirs)
			if err == nil {
				cfgLoaded = true
			}
		}
	}

	// Settings checks always run, reading settings.cue directly.
	var settingsCfg cue.Value
	if cfgLoaded {
		settingsCfg = cfgResult.Value
	}
	report.Sections = append(report.Sections, doctor.CheckSettings(paths, settingsCfg))

	if cfgLoaded {
		report.Sections = append(report.Sections, doctor.CheckAgents(cfgResult.Value))
	} else {
		report.Sections = append(report.Sections, doctor.SectionResult{
			Name: "Agents",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "no valid config"},
			},
		})
	}

	if cfgLoaded {
		report.Sections = append(report.Sections, doctor.CheckRoles(cfgResult.Value))
	} else {
		report.Sections = append(report.Sections, doctor.SectionResult{
			Name: "Roles",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "no valid config"},
			},
		})
	}

	if cfgLoaded {
		report.Sections = append(report.Sections, doctor.CheckContexts(cfgResult.Value))
	} else {
		report.Sections = append(report.Sections, doctor.SectionResult{
			Name: "Contexts",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "no valid config"},
			},
		})
	}

	if cfgLoaded {
		report.Sections = append(report.Sections, doctor.CheckTasks(cfgResult.Value))
	} else {
		report.Sections = append(report.Sections, doctor.SectionResult{
			Name: "Tasks",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "no valid config"},
			},
		})
	}

	scan := doctor.SkillDestScan{}
	if needsSkillDestScan(paths) {
		scan = skillDestScan(cmd)
	}
	report.Sections = append(report.Sections, doctor.CheckSkills(paths, scan))

	report.Sections = append(report.Sections, doctor.CheckAliases(config.AliasStorePath(paths), reserved))

	report.Sections = append(report.Sections, doctor.CheckEnvironment(paths))

	return report, nil
}

func skillDestScan(cmd *cobra.Command) doctor.SkillDestScan {
	global, local, err := scanSkillUninstallRoots(cmd)
	if err != nil {
		return doctor.SkillDestScan{Scanned: true, Err: err}
	}
	return doctor.SkillDestScan{Scanned: true, Global: global, Local: local}
}

func fetchAndValidateSchemas(paths config.Paths, provider clientProvider) doctor.SectionResult {
	if !paths.AnyExists() {
		return doctor.SectionResult{
			Name: "Schema Validation",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "no config directories"},
			},
		}
	}

	client, err := provider()
	if err != nil {
		return doctor.SectionResult{
			Name: "Schema Validation",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "registry unavailable"},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resolvedPath, err := client.ResolveLatestVersion(ctx, registry.SchemaModulePath)
	if err != nil {
		return doctor.SectionResult{
			Name: "Schema Validation",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "cannot resolve schema version"},
			},
		}
	}

	result, err := client.Fetch(ctx, resolvedPath)
	if err != nil {
		return doctor.SectionResult{
			Name: "Schema Validation",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: "cannot fetch schemas"},
			},
		}
	}

	schemas, err := doctor.LoadSchemas(result.SourceDir, client.Registry())
	if err != nil {
		return doctor.SectionResult{
			Name: "Schema Validation",
			Results: []doctor.CheckResult{
				{Status: doctor.StatusInfo, Label: "Skipped", Message: fmt.Sprintf("cannot load schemas: %v", err)},
			},
		}
	}

	return doctor.CheckSchemaValidation(paths, schemas)
}

// resolveIndexVersion returns the latest index version string. Reads cache first
// to avoid a network call, falling back to a registry query.
func resolveIndexVersion(indexPath string, provider clientProvider) string {
	cached, err := cache.ReadIndex()
	if err == nil && cached.Version != "" {
		return modules.VersionFromOrigin(cached.Version)
	}

	client, err := provider()
	if err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resolved, err := client.ResolveLatestVersion(ctx, registry.EffectiveIndexPath(indexPath))
	if err != nil {
		return ""
	}

	return modules.VersionFromOrigin(resolved)
}
