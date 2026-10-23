package llm

import (
	"alfa.local/shared/platform"
	"embed"
)

//go:embed prompts/*.txt
var prompts embed.FS

func Prompt(task string) (string, error) {
	if !Contains([]string{"tag", "profile", "insight", "final", "check", "chat"}, task) {
		return "", ErrInvalid
	}
	fileTask := task
	if task == "final" {
		fileTask = "insight"
	}
	b, e := prompts.ReadFile("prompts/" + PromptVersion(fileTask) + ".txt")
	return string(b), e
}
func promptHash(task string) string { p, _ := Prompt(task); return platform.Hash(p) }
