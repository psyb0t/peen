package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	maskedCallMaxBytes = 256
	maskedPathMaxBytes = 96
)

func maskedCallDescription(messages []elelem.Message, id string) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != elelem.RoleAssistant {
			continue
		}

		for _, call := range message.ToolCalls {
			if call.ID != id {
				continue
			}

			arguments := string(call.Arguments)
			if call.Name == toolNameReadFile {
				var window struct {
					Path   string `json:"path"`
					Offset int    `json:"offset"`
					Limit  int    `json:"limit"`
				}
				if json.Unmarshal(call.Arguments, &window) == nil {
					arguments = fmt.Sprintf("path=%q offset=%d limit=%d",
						workflowUTF8Prefix(window.Path, maskedPathMaxBytes),
						max(window.Offset, 1), window.Limit)
				}
			}

			description := workflowUTF8Prefix(call.Name+" "+arguments,
				maskedCallMaxBytes)
			// Escape controls and tags inside the wrapper.
			encoded, err := json.Marshal(description)
			if err != nil {
				return ""
			}

			return workflowUTF8Prefix(strings.TrimSpace(string(encoded)),
				maskedCallMaxBytes)
		}
	}

	return ""
}
