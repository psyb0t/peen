package openaicompat

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/psyb0t/peen/internal/pkg/agent"
)

func (h handler) mapMessages(
	messages []message,
	includeHistory bool,
) (agent.MessageRequest, *responseError) {
	request := agent.MessageRequest{}
	messages = developerAsSystem(messages)

	if len(messages) == 0 || messages[len(messages)-1].Role != roleUser {
		problem := invalidField("messages")

		return request, &problem
	}

	start := 0

	if !includeHistory {
		start = len(messages) - 1
	}

	transcript := priorConversation{}
	systems := []string{}

	for index := start; index < len(messages); index++ {
		text, images, problem := h.parseMessage(messages[index],
			len(request.Images))
		if problem != nil {
			return request, problem
		}

		request.Images = append(request.Images, images...)
		if index == len(messages)-1 {
			request.Message = text

			continue
		}

		if messages[index].Role == roleSystem {
			systems = append(systems, text)

			continue
		}

		transcript.Messages = append(transcript.Messages, priorMessage{
			Role: messages[index].Role, Body: text,
		})
	}

	if strings.TrimSpace(request.Message) == "" {
		problem := invalidField("messages.content")

		return request, &problem
	}

	return assembleHistory(request, transcript, systems)
}

// developerAsSystem treats the developer role, which newer OpenAI clients send
// in place of system, as system.
func developerAsSystem(messages []message) []message {
	mapped := make([]message, len(messages))
	copy(mapped, messages)

	for index := range mapped {
		if mapped[index].Role == roleDeveloper {
			mapped[index].Role = roleSystem
		}
	}

	return mapped
}

func assembleHistory(
	request agent.MessageRequest,
	transcript priorConversation,
	systems []string,
) (agent.MessageRequest, *responseError) {
	if len(transcript.Messages) != 0 {
		encoded, err := xml.Marshal(transcript)
		if err != nil {
			problem := invalidField("messages.content")

			return request, &problem
		}

		request.Message = string(encoded) + messageSeparator + request.Message
	}

	system := strings.Join(systems, messageSeparator)
	if strings.TrimSpace(system) != "" {
		request.SystemPrompt = &agent.MessageSystemPrompt{
			Content: system, Mode: agent.PromptModeAppend,
		}
	}

	return request, nil
}

func (h handler) parseMessage(
	input message,
	imageOffset int,
) (string, []agent.MessageImage, *responseError) {
	if input.Role != roleSystem && input.Role != roleUser &&
		input.Role != roleAssistant {
		problem := invalidField("messages.role")

		return "", nil, &problem
	}

	var text string

	if err := json.Unmarshal(input.Content, &text); err == nil {
		return text, nil, nil
	}

	var parts []contentPart

	if err := json.Unmarshal(input.Content, &parts); err != nil ||
		len(parts) == 0 {
		problem := invalidField("messages.content")

		return "", nil, &problem
	}

	return h.parseParts(input.Role, parts, imageOffset)
}

func (h handler) parseParts(
	role string,
	parts []contentPart,
	imageOffset int,
) (string, []agent.MessageImage, *responseError) {
	text := []string{}
	images := []agent.MessageImage{}

	for _, part := range parts {
		switch part.Type {
		case partText:
			text = append(text, part.Text)
		case partImage:
			if role != roleUser || part.ImageURL == nil ||
				imageOffset+len(images) >= h.dependencies.MaxImages {
				problem := invalidField("messages.content.image_url")

				return "", nil, &problem
			}

			image, problem := h.decodeImage(part.ImageURL.URL)
			if problem != nil {
				return "", nil, problem
			}

			images = append(images, image)
			text = append(text, fmt.Sprintf(imagePlaceholder,
				imageOffset+len(images)))
		default:
			problem := invalidField("messages.content.type")

			return "", nil, &problem
		}
	}

	return strings.Join(text, ""), images, nil
}
