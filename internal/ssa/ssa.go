// Package ssa applies ApplyConfiguration mutations to Kubernetes objects using
// the same server-side-apply (structured-merge-diff) logic as the API server.
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
	"errors"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
)

// errUnexpectedMergedType is returned when the merged runtime.Object is not the
// expected *unstructured.Unstructured.
var errUnexpectedMergedType = errors.New("merged object has unexpected type")

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

// Merge applies patch onto live using server-side-apply semantics and returns
// the merged object. The patch inherits live's GroupVersionKind, since
// ApplyConfiguration expressions typically omit apiVersion/kind.
func Merge(live, patch *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	preparedPatch := patch.DeepCopy()
	preparedPatch.SetGroupVersionKind(live.GroupVersionKind())

	converter, err := builtinConverter()
	if err != nil {
		return nil, err
	}

	merged, err := apply(converter, live, preparedPatch)
	if err != nil && strings.Contains(err.Error(), errNoSchema) {
		// Unknown kind (e.g. a CRD): merge without a schema, treating lists as atomic.
		return apply(deducedTC, live, preparedPatch)
	}

	return merged, err
}

// apply performs the structured-merge-diff merge with the given type converter.
func apply(
	converter managedfields.TypeConverter,
	live, patch *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	liveTyped, err := converter.ObjectToTyped(live, typed.AllowDuplicates)
	if err != nil {
		return nil, fmt.Errorf("convert object to typed: %w", err)
	}

	patchTyped, err := converter.ObjectToTyped(patch)
	if err != nil {
		return nil, fmt.Errorf("convert apply configuration to typed: %w", err)
	}

	mergedTyped, err := liveTyped.Merge(patchTyped)
	if err != nil {
		return nil, fmt.Errorf("merge apply configuration: %w", err)
	}

	merged, err := converter.TypedToObject(mergedTyped)
	if err != nil {
		return nil, fmt.Errorf("convert merged object from typed: %w", err)
	}

	result, ok := merged.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errUnexpectedMergedType, merged)
	}

	return result, nil
}
