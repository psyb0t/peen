package server

//go:generate bash ../../../../scripts/web/run.sh generate
//go:generate go tool gen-asyncapi -language=typescript -o=../../../../web/src/lib/ws/generated.ts ../../../../api/asyncapi.yaml
