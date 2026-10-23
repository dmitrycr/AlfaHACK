// Emit synthetic contract examples for docs and schema validation.
package main

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"context"
	"encoding/json"
	"os"
)

func main() {
	in := llmtest.Context()
	mock := llm.Mock{}
	run := func(task string, input, out any) {
		b, _ := json.Marshal(input)
		r, _, e := mock.Complete(context.Background(), llm.Call{Task: task, Scope: "company:company-test", Input: b})
		if e != nil {
			panic(e)
		}
		if e = llm.Strict(r, out); e != nil {
			panic(e)
		}
	}
	profileInput := llm.ProfileInput{Company: in.Company, Profile: in.Profile, Features: in.Features}
	var brief llm.ProfileBrief
	run("profile", profileInput, &brief)
	in.BusinessProfile = &brief
	var card llm.Card
	run("insight", in, &card)
	checkInput := llm.CheckInput{Context: in, Card: card}
	var check llm.CheckResult
	run("check", checkInput, &check)
	insight := llm.Insight{ID: "insight-test", CompanyID: in.Company.ID, EventID: in.Prepared.Event.EventID, EventVersion: 2, ProfileVersion: 1, RulesetVersion: in.Match.RulesetVersion, RunID: "mock-generation-run", CheckRunID: "mock-check-run", Synthetic: true, Card: card, Check: check}
	insight.WorkflowID = "mock-workflow"
	insight.ProfileRunID = "mock-profile-run"
	insight.BusinessProfile = &brief
	insight.Sources = in.Prepared.Event.Sources
	insight.ProfileFeatures = in.Features
	chat := llm.ChatInput{Context: in, Insight: insight, History: []llm.Message{}, Question: "Что произошло в новости?"}
	var answer llm.Answer
	run("chat", chat, &answer)
	tag := llmtest.Tag()
	var tagResult llm.TagResult
	run("tag", tag, &tagResult)
	examples := map[string]any{"profile-input": profileInput, "profile-brief": brief, "tag-input": tag, "tag-result": tagResult, "prepared-event": in.Prepared, "insight-context": in, "card": card, "check-input": checkInput, "check-result": check, "insight": insight, "chat-input": chat, "chat-answer": answer, "match": in.Match}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if e := encoder.Encode(examples); e != nil {
		panic(e)
	}
}
