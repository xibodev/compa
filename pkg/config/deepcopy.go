package config

import "reflect"

var channelType = reflect.TypeOf(Channel{})

// deepCopy returns a copy of v that shares nothing a change through the copy
// could reach: pointers, maps, slices and interfaces are copied as well.
// Unexported struct fields are copied as they are, except a Channel's
// decoded settings, which are copied deeply too.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopy(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopy(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		if v.Type() == channelType {
			if ch := out.Addr().Interface().(*Channel); ch.extend != nil {
				ch.extend = deepCopy(reflect.ValueOf(ch.extend)).Interface()
			}
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return out
	default:
		// A Value read from a struct field refers to that field; copy it.
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		return out
	}
}

// equalSettingValues reports whether two values of a setting are equal,
// counting a nil map or slice as equal to an empty one.
func equalSettingValues(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Map, reflect.Slice:
		if a.Len() == 0 && b.Len() == 0 {
			return true
		}
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}
