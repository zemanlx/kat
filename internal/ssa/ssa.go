// Package ssa provides the type converter with which ApplyConfiguration
// mutations are merged the way the API server merges them.
//
// Merge behaviour is driven by the OpenAPI schema embedded in this package, so
// lists declared as listType=map (containers, env, ports, volumes, ...) are
// merged by their keys instead of being replaced wholesale. Objects whose kind
// is not present in the built-in schema (typically custom resources) fall back
// to a schemaless merge in which every list is treated as atomic, matching how
// the API server treats CRDs without a structural schema.
package ssa

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
)

// builtinSwagger is the Kubernetes OpenAPI v2 schema for built-in types, used to
// resolve list merge keys. It is version-locked to the k8s.io/* dependencies
// (see schema.version) and parsed once when the type converter is first built.
//
//go:embed swagger.json
var builtinSwagger []byte

// errNoSchema is the substring the type converter uses to report an object whose
// GroupVersionKind is absent from the schema (see managedfields'
// noCorrespondingTypeErr). It selects the schemaless fallback for such objects.
const errNoSchema = "no corresponding type"

//nolint:gochecknoglobals // Converters are built once from the embedded schema.
var (
	// builtinConverter lazily builds a TypeConverter from the embedded schema.
	builtinConverter = sync.OnceValues(buildBuiltinConverter)

	// deducedTC merges schemaless objects (CRDs) with all lists treated as atomic.
	deducedTC = managedfields.NewDeducedTypeConverter()
)

// buildBuiltinConverter parses the embedded schema into a TypeConverter. It runs
// once, guarded by the sync.OnceValues wrapper on builtinConverter.
func buildBuiltinConverter() (managedfields.TypeConverter, error) {
	var swagger spec.Swagger
	if err := json.Unmarshal(builtinSwagger, &swagger); err != nil {
		return nil, fmt.Errorf("parse embedded OpenAPI schema: %w", err)
	}

	models := make(map[string]*spec.Schema, len(swagger.Definitions))
	for name := range swagger.Definitions {
		model := swagger.Definitions[name]
		models[name] = &model
	}

	converter, err := managedfields.NewTypeConverter(models, false)
	if err != nil {
		return nil, fmt.Errorf("build type converter from schema: %w", err)
	}

	return converter, nil
}

// TypeConverter returns the type converter the API server would use for an
// object: the built-in schema for built-in kinds, and a schemaless one, in
// which every list is atomic, for any other kind.
func TypeConverter() (managedfields.TypeConverter, error) {
	converter, err := builtinConverter()
	if err != nil {
		return nil, err
	}

	return fallbackConverter{converter}, nil
}

// fallbackConverter converts objects of kinds missing from the built-in
// schema with the schemaless converter.
type fallbackConverter struct {
	managedfields.TypeConverter
}

func (c fallbackConverter) ObjectToTyped(obj runtime.Object, opts ...typed.ValidationOptions) (*typed.TypedValue, error) {
	// Errors pass through unwrapped: the server's patcher reports them as is.
	tv, err := c.TypeConverter.ObjectToTyped(obj, opts...)
	if err != nil && strings.Contains(err.Error(), errNoSchema) {
		return deducedTC.ObjectToTyped(obj, opts...) //nolint:wrapcheck // See above.
	}

	return tv, err //nolint:wrapcheck // See above.
}
