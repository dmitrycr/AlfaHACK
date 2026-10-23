// Generate the backend LLM wire schema without contacting external services.
package main

import (
	"alfa.local/shared/llm"
	"encoding/json"
	"os"
)

func main() {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(llm.JSONSchema()); err != nil {
		panic(err)
	}
}
