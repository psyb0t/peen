package main

import (
	"flag"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"gorm.io/gen"
)

const (
	defaultOutputPath   = "internal/pkg/db/repositories"
	queryTimeImportPath = "time"
	queryUUIDImportPath = "github.com/google/uuid"
)

func main() {
	outputPath := flag.String(
		"out",
		defaultOutputPath,
		"repository output directory",
	)

	flag.Parse()

	generator := gen.NewGenerator(gen.Config{
		OutPath: *outputPath,
		OutFile: "repositories.gen.go",
		Mode: gen.WithoutContext |
			gen.WithDefaultQuery |
			gen.WithQueryInterface,
	})

	// Fix import order before unordered custom parameter discovery.
	generator.WithImportPkgPath(queryTimeImportPath, queryUUIDImportPath)

	generator.ApplyBasic(
		models.AgentRun{},
		models.AgentRunCompaction{},
		models.AgentRunEvent{},
		models.AgentRunMessage{},
		models.Compaction{},
		models.ContextSnapshot{},
		models.Event{},
		models.Execution{},
		models.ExecutionActivity{},
		models.ExecutionEvent{},
		models.ExecutionInboxMessage{},
		models.ExecutionTurnLease{},
		models.Job{},
		models.JobOutputLine{},
		models.JobSignalRequest{},
		models.Message{},
		models.ModelCall{},
		models.ModelRun{},
		models.PromptSnapshot{},
		models.Session{},
		models.SessionNotice{},
		models.SessionAgentSpaceDecision{},
		models.Turn{},
		models.TurnOpeningNotice{},
		models.WorkerGeneration{},
	)
	generator.ApplyInterface(func(CompactionQuerier) {}, models.Compaction{})
	generator.ApplyInterface(func(MessageQuerier) {}, models.Message{})
	generator.ApplyInterface(func(ExecutionQuerier) {}, models.Execution{})
	generator.ApplyInterface(
		func(ExecutionInboxMessageQuerier) {}, models.ExecutionInboxMessage{},
	)
	generator.ApplyInterface(
		func(AgentRunCompactionQuerier) {},
		models.AgentRunCompaction{},
	)
	generator.ApplyInterface(
		func(AgentRunEventQuerier) {},
		models.AgentRunEvent{},
	)
	generator.Execute()
}
