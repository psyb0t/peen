package main

import (
	"flag"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"gorm.io/gen"
)

const defaultOutputPath = "internal/pkg/db/repositories"

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

	generator.ApplyBasic(
		models.Compaction{},
		models.ContextSnapshot{},
		models.Event{},
		models.Message{},
		models.PromptSnapshot{},
		models.Session{},
		models.Turn{},
	)
	generator.ApplyInterface(func(CompactionQuerier) {}, models.Compaction{})
	generator.ApplyInterface(func(MessageQuerier) {}, models.Message{})
	generator.Execute()
}
