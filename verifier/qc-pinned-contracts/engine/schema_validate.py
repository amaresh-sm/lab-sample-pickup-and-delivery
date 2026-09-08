"""Dependency-free JSON Schema validator for the subset actually used by
tasks/lab-sample-pickup-and-delivery/openapi.contract.json: $ref, allOf,
oneOf, type, properties, required, additionalProperties, enum, items,
minLength, minimum, maximum, exclusiveMinimum. `format` is read but NOT
validated (no date-time/uuid format checkers) -- a deliberate scope
limitation, not an oversight.

Not a general-purpose JSON Schema implementation. `jsonschema` (the real
library) is not installed inside a candidate's own container -- only on
the host -- and this engine deliberately never adds dependencies to the
environment it runs its checks from (see engine/README.md), so this exists
purely to cover what THIS contract's schemas actually contain.
"""
from __future__ import annotations

from typing import Any

_TYPE_MAP = {
    "string": str,
    "boolean": bool,
    "object": dict,
    "array": list,
    # integer/number handled specially below (bool is an int subclass in
    # Python, and a schema-legal integer must not accidentally accept True/False)
}


def _resolve(schema: dict, components: dict) -> dict:
    if "$ref" in schema:
        name = schema["$ref"].rsplit("/", 1)[-1]
        return components.get(name, {})
    return schema


def validate(instance: Any, schema: dict, components: dict, path: str = "$") -> list[str]:
    """Return a list of human-readable violations; an empty list means valid."""
    schema = _resolve(schema, components)
    errors: list[str] = []

    if "allOf" in schema:
        for sub in schema["allOf"]:
            errors += validate(instance, sub, components, path)
        rest = {k: v for k, v in schema.items() if k != "allOf"}
        if rest:
            errors += validate(instance, rest, components, path)
        return errors

    if "oneOf" in schema:
        matches = sum(1 for sub in schema["oneOf"] if not validate(instance, sub, components, path))
        if matches != 1:
            errors.append(f"{path}: expected exactly one oneOf branch to match, {matches} did")
        return errors

    expected_type = schema.get("type")
    if expected_type in ("integer", "number"):
        if isinstance(instance, bool) or not isinstance(instance, (int, float)):
            errors.append(f"{path}: expected type {expected_type!r}, got {type(instance).__name__}")
            return errors
        if expected_type == "integer" and not isinstance(instance, int):
            errors.append(f"{path}: expected type 'integer', got {type(instance).__name__}")
            return errors
    elif expected_type:
        py_type = _TYPE_MAP.get(expected_type)
        if py_type is not None and not isinstance(instance, py_type):
            errors.append(f"{path}: expected type {expected_type!r}, got {type(instance).__name__}")
            return errors  # further checks are meaningless once the type itself is wrong

    if "enum" in schema and instance not in schema["enum"]:
        errors.append(f"{path}: value {instance!r} not in enum {schema['enum']!r}")

    if isinstance(instance, str) and "minLength" in schema and len(instance) < schema["minLength"]:
        errors.append(f"{path}: string shorter than minLength {schema['minLength']}")

    if isinstance(instance, (int, float)) and not isinstance(instance, bool):
        # `exclusiveMinimum`/`exclusiveMaximum` has TWO incompatible meanings
        # depending on OpenAPI/JSON-Schema version, and this contract uses
        # the OLDER OpenAPI 3.0 one: a BOOLEAN modifier on `minimum`/`maximum`
        # (`{"minimum": 0, "exclusiveMinimum": true}` means "> 0", not a
        # standalone numeric threshold the way JSON Schema draft-06+ uses it).
        # Confirmed against every occurrence actually in this contract
        # (Location.coverageKm) before fixing -- treating it as a number
        # produced a false positive: Python silently coerces `True` to `1` in
        # a numeric comparison, so `0 <= exclusiveMinimum(true)` read as
        # `0 <= 1` and wrongly flagged a genuinely valid value.
        exclusive_min = schema.get("exclusiveMinimum")
        exclusive_max = schema.get("exclusiveMaximum")
        min_bound = schema.get("minimum")
        max_bound = schema.get("maximum")
        if isinstance(exclusive_min, bool):
            if exclusive_min and min_bound is not None and instance <= min_bound:
                errors.append(f"{path}: {instance} <= exclusive minimum {min_bound}")
            elif not exclusive_min and min_bound is not None and instance < min_bound:
                errors.append(f"{path}: {instance} < minimum {min_bound}")
        else:
            if min_bound is not None and instance < min_bound:
                errors.append(f"{path}: {instance} < minimum {min_bound}")
            if isinstance(exclusive_min, (int, float)) and instance <= exclusive_min:
                errors.append(f"{path}: {instance} <= exclusiveMinimum {exclusive_min}")
        if isinstance(exclusive_max, bool):
            if exclusive_max and max_bound is not None and instance >= max_bound:
                errors.append(f"{path}: {instance} >= exclusive maximum {max_bound}")
            elif not exclusive_max and max_bound is not None and instance > max_bound:
                errors.append(f"{path}: {instance} > maximum {max_bound}")
        else:
            if max_bound is not None and instance > max_bound:
                errors.append(f"{path}: {instance} > maximum {max_bound}")
            if isinstance(exclusive_max, (int, float)) and instance >= exclusive_max:
                errors.append(f"{path}: {instance} >= exclusiveMaximum {exclusive_max}")

    if isinstance(instance, dict):
        props = schema.get("properties") or {}
        for req in schema.get("required") or []:
            if req not in instance:
                errors.append(f"{path}: missing required property {req!r}")
        if schema.get("additionalProperties") is False:
            allowed = set(props.keys())
            for key in instance:
                if key not in allowed:
                    errors.append(f"{path}: additional property {key!r} not allowed")
        for key, val in instance.items():
            if key in props:
                errors += validate(val, props[key], components, f"{path}.{key}")

    if isinstance(instance, list) and "items" in schema:
        for i, item in enumerate(instance):
            errors += validate(item, schema["items"], components, f"{path}[{i}]")

    return errors
