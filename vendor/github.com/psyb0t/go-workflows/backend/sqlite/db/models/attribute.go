package models

type Attribute struct {
	ID          string
	InstanceID  string
	ExecutionID string
	Data        []byte
}

func (Attribute) TableName() string { return "attributes" }
