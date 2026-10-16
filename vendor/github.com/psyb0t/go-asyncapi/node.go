package asyncapi

import (
	"strconv"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"gopkg.in/yaml.v3"
)

// yamlNodesPerMappingEntry is the node count of one mapping entry: a YAML
// mapping node holds its keys and values alternately in Content.
const yamlNodesPerMappingEntry = 2

// Object is a JSON/YAML mapping that keeps its keys in the order the document
// lists them. Values are string, int64, float64, bool, nil, []any or *Object.
type Object struct {
	Keys   []string
	Values map[string]any
}

// Get returns the value stored under key and whether it exists.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}

	value, ok := o.Values[key]

	return value, ok
}

// String returns the string stored under key, or "" when it is missing or not
// a string.
func (o *Object) String(key string) string {
	value, _ := o.Get(key)
	text, _ := value.(string)

	return text
}

// Object returns the mapping stored under key, or nil.
func (o *Object) Object(key string) *Object {
	value, _ := o.Get(key)
	child, _ := value.(*Object)

	return child
}

// fromNode converts a decoded YAML node into plain Go values: *Object for
// mappings, []any for sequences, and string, int64, float64, bool or nil for
// scalars.
func fromNode(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nullValue(), nil
		}

		return fromNode(node.Content[0])
	case yaml.MappingNode:
		return mappingFromNode(node)
	case yaml.SequenceNode:
		items := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			item, err := fromNode(child)
			if err != nil {
				return nil, err
			}

			items = append(items, item)
		}

		return items, nil
	case yaml.ScalarNode:
		return scalarFromNode(node)
	case yaml.AliasNode:
		return fromNode(node.Alias)
	default:
		return nil, ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"unsupported yaml node kind %d at line %d",
			node.Kind,
			node.Line,
		)
	}
}

// nullValue is the decoded form of a YAML null and of an empty document.
func nullValue() any {
	return nil
}

func mappingFromNode(node *yaml.Node) (*Object, error) {
	pairCount := len(node.Content) / yamlNodesPerMappingEntry
	object := &Object{Values: make(map[string]any, pairCount)}

	for i := 0; i+1 < len(node.Content); i += yamlNodesPerMappingEntry {
		keyNode := node.Content[i]

		value, err := fromNode(node.Content[i+1])
		if err != nil {
			return nil, err
		}

		if _, exists := object.Values[keyNode.Value]; !exists {
			object.Keys = append(object.Keys, keyNode.Value)
		}

		object.Values[keyNode.Value] = value
	}

	return object, nil
}

func scalarFromNode(node *yaml.Node) (any, error) {
	// ShortTag resolves the implicit tag of an untagged scalar, so "3" is
	// !!int and "x" is !!str whether or not the document spelled the tag.
	switch node.ShortTag() {
	case "!!null":
		return nullValue(), nil
	case "!!bool":
		value, err := strconv.ParseBool(node.Value)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "parse bool at line %d", node.Line)
		}

		return value, nil
	case "!!int":
		value, err := strconv.ParseInt(node.Value, 0, 64)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "parse int at line %d", node.Line)
		}

		return value, nil
	case "!!float":
		value, err := strconv.ParseFloat(node.Value, 64)
		if err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"parse float at line %d",
				node.Line,
			)
		}

		return value, nil
	default:
		return node.Value, nil
	}
}
