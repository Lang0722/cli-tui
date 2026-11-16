package protocol

// Skills (Codex skills/list and skill mentions).
//
// A skill is a SKILL.md file the agent can load on request. The backend
// owns the catalog: the UI asks for it with ListSkills (when a session is
// configured, when its directory changes, and after the user closes the
// Enable/Disable Skills view) and shows it in the "$" mention popup and in
// /skills. A backend can also push SkillsList at any time. Backends without
// skills can ignore these ops; the UI then shows no skills.

// Skill is one catalog entry.
type Skill struct {
	// Name is what "$name" mentions use, e.g. "repo-scout". A plugin
	// skill may be namespaced as "plugin:skill" (shown "skill (plugin)").
	Name string `json:"name"`
	// DisplayName replaces Name in lists when set.
	DisplayName string `json:"display_name,omitempty"`
	// Description is shown next to the name (ShortDescription wins).
	Description      string `json:"description,omitempty"`
	ShortDescription string `json:"short_description,omitempty"`
	// Path is the skill's SKILL.md (absolute). It identifies the skill in
	// SetSkillEnabled and UserInput.Skills.
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
	// Scope is informational ("user", "repo", "system", ...).
	Scope string `json:"scope,omitempty"`
}

// SkillError reports a SKILL.md that could not be loaded.
type SkillError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ListSkills asks for the skills available in Cwd. Reply with SkillsList.
// ForceReload asks the backend to re-read SKILL.md files from disk.
type ListSkills struct {
	Cwd         string `json:"cwd,omitempty"`
	ForceReload bool   `json:"force_reload,omitempty"`
}

// SkillsList answers ListSkills (or is pushed unprompted) with the whole
// catalog, disabled skills included. Errors are shown once as warnings
// ("Skipped loading N skill(s) due to invalid SKILL.md files.").
type SkillsList struct {
	Skills []Skill      `json:"skills"`
	Errors []SkillError `json:"errors,omitempty"`
}

// SetSkillEnabled is sent when the user toggles a skill in /skills →
// Enable/Disable Skills. The backend persists it; a failure can be
// reported with an Error event.
type SetSkillEnabled struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// SkillRef names a skill the user mentioned ("$name") in a UserInput.
type SkillRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (SkillsList) eventType() string   { return "skills_list" }
func (ListSkills) opType() string      { return "list_skills" }
func (SetSkillEnabled) opType() string { return "set_skill_enabled" }
