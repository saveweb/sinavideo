package main

import "go.uber.org/zap/zapcore"

type omitFieldsCore struct {
	zapcore.Core
	omitted map[string]struct{}
}

func omitFields(core zapcore.Core, keys ...string) zapcore.Core {
	omitted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		omitted[key] = struct{}{}
	}
	return omitFieldsCore{Core: core, omitted: omitted}
}

func (core omitFieldsCore) With(fields []zapcore.Field) zapcore.Core {
	return omitFieldsCore{
		Core:    core.Core.With(core.filter(fields)),
		omitted: core.omitted,
	}
}

func (core omitFieldsCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checked.AddCore(entry, core)
	}
	return checked
}

func (core omitFieldsCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	return core.Core.Write(entry, core.filter(fields))
}

func (core omitFieldsCore) filter(fields []zapcore.Field) []zapcore.Field {
	for i, field := range fields {
		if _, ok := core.omitted[field.Key]; !ok {
			continue
		}

		filtered := make([]zapcore.Field, 0, len(fields)-1)
		filtered = append(filtered, fields[:i]...)
		for _, candidate := range fields[i+1:] {
			if _, ok := core.omitted[candidate.Key]; !ok {
				filtered = append(filtered, candidate)
			}
		}
		return filtered
	}
	return fields
}
