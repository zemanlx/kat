package loader

import "errors"

var (
	ErrCreateRequiresObject         = errors.New("operation CREATE requires object data")
	ErrDeleteRequiresOldObject      = errors.New("operation DELETE requires oldObject data")
	ErrUpdateRequiresObject         = errors.New("operation UPDATE requires object data")
	ErrUpdateRequiresOldObject      = errors.New("operation UPDATE requires oldObject data")
	ErrUnknownOperation             = errors.New("unknown operation")
	ErrCannotInferOperation         = errors.New("cannot infer operation")
	ErrUnknownFileType              = errors.New("unknown file type")
	ErrUnsupportedV1Beta1Policy     = errors.New("ValidatingAdmissionPolicy v1beta1 not supported, use v1")
	ErrUnsupportedV1Beta1Binding    = errors.New("ValidatingAdmissionPolicyBinding v1beta1 not supported, use v1")
	ErrUnsupportedV1Beta1MutPolicy  = errors.New("MutatingAdmissionPolicy v1beta1 not supported, use v1")
	ErrUnsupportedV1Beta1MutBinding = errors.New("MutatingAdmissionPolicyBinding v1beta1 not supported, use v1")
	ErrConflictObject               = errors.New("conflict: object defined in multiple files")
	ErrConflictOldObject            = errors.New("conflict: oldObject defined in multiple files")
	ErrConflictNamespaceObject      = errors.New("conflict: namespaceObject defined in multiple files")
	ErrConflictParams               = errors.New("conflict: params defined in multiple files")
)
