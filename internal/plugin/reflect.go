package plugin

import "reflect"

// implementsReflect reports whether v's concrete type satisfies the interface
// described by ifacePtr.
//
// ifacePtr must be a value of the form `(*SomeInterface)(nil)` — i.e. a typed
// nil whose type is a pointer to an interface. We extract that interface type
// and ask whether v's type is assignable to it.
//
// Usage:
//
//	r.WithCapability((*Poller)(nil))
//
// This keeps the registry call-site cheap and free of reflection in the hot
// path (only the type check uses reflect, done per-call).
func implementsReflect(ifacePtr, v interface{}) bool {
	if ifacePtr == nil {
		return false
	}
	t := reflect.TypeOf(ifacePtr)
	if t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Interface {
		// Caller passed something that is not `(*Interface)(nil)`.
		// As a fallback, treat ifacePtr as the interface value directly.
		if t.Kind() == reflect.Interface {
			if v == nil {
				return false
			}
			return reflect.TypeOf(v).Implements(t)
		}
		return false
	}
	ifaceType := t.Elem()
	if v == nil {
		return false
	}
	return reflect.TypeOf(v).Implements(ifaceType)
}
