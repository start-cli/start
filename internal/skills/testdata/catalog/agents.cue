package catalog

agents: "claude-code": {
	name: "Claude Code"
	bin:  "claude"
	config: {
		global: "~/.claude"
		local:  ".claude"
	}
	skills: {
		global: {
			native: "~/.claude/skills"
			agents: "~/.agents/skills"
		}
		local: {
			native: ".claude/skills"
			agents: ".agents/skills"
		}
	}
	provider: ["anthropic"]
}

agents: "agy": {
	name: "Agy"
	bin:  "agy"
	config: {
		global: "~/.agy"
		local:  ".agy"
	}
	skills: {
		global: {
			agents: "~/.agents/skills"
		}
		local: {
			agents: ".agents/skills"
		}
	}
	provider: ["anthropic"]
}

agents: "noskills": {
	name: "No Skills"
	bin:  "noskills"
	config: {
		global: "~/.noskills"
	}
	provider: ["openai"]
}

agents: "local-only": {
	name: "Local Only"
	bin:  "local-only"
	config: {
		global: "~/.local-only"
		local:  ".local-only"
	}
	skills: {
		local: {
			agents: ".agents/skills"
		}
	}
	provider: ["openai"]
}

agents: "alt-only": {
	name: "Alternatives Only"
	bin:  "alt-only"
	config: {
		global: "~/.alt"
		local:  ".alt"
	}
	skills: {
		global: {
			alternatives: ["~/.alt/skills"]
		}
		local: {
			alternatives: [".alt/skills"]
		}
	}
	provider: ["openai"]
}
