#!/usr/bin/env python3
"""Offline OpenAPI/schema/runtime checks and a conservative compatibility gate."""
import argparse
import datetime
import json
from pathlib import Path
import re
import sys
import yaml
from jsonschema import Draft202012Validator, FormatChecker, ValidationError, validators

ROOT = Path(__file__).resolve().parents[1]
METHODS = {"get", "post", "delete", "put", "patch", "head", "options", "trace"}

def max_bytes(validator, limit, instance, schema):
    if isinstance(instance, str) and schema.get("x-trimSpace"):
        # unicode.IsSpace, used by Go's strings.TrimSpace (Unicode White_Space).
        instance = instance.strip("\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000")
    if isinstance(instance, str) and len(instance.encode("utf-8")) > limit:
        yield ValidationError(f"string exceeds {limit} UTF-8 bytes")

Validator = validators.extend(Draft202012Validator, {"x-maxBytes": max_bytes})
FORMATS = FormatChecker()

@FORMATS.checks("date-time", raises=ValueError)
def date_time(value):
    if not isinstance(value, str):
        return True
    match = re.fullmatch(r"([0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.([0-9]+))?([Zz]|[+-][0-9]{2}:[0-9]{2})", value)
    if not match:
        return False
    clock, fraction, zone = match.groups()
    if zone.upper() == "Z":
        zone = "+00:00"
    elif int(zone[1:3]) > 23 or int(zone[4:6]) > 59:
        return False
    # Python 3.10 only accepts three or six fractional digits. RFC3339/Go
    # permit arbitrary precision; truncation/padding here changes no calendar
    # validation and is not a conversion of the actual response timestamp.
    normalized = clock.upper()
    if fraction:
        normalized += "." + fraction[:6].ljust(6, "0")
    datetime.datetime.fromisoformat(normalized + zone)
    return True

def pointer(document, ref):
    if not ref.startswith("#/"):
        raise ValueError(f"only offline local references are allowed: {ref}")
    value = document
    for part in ref[2:].split("/"):
        value = value[part.replace("~1", "/").replace("~0", "~")]
    return value

def resolve(document, value):
    while isinstance(value, dict) and "$ref" in value:
        value = pointer(document, value["$ref"])
    return value

def walk(document, value):
    if isinstance(value, dict):
        if "$ref" in value:
            pointer(document, value["$ref"])
            annotations = {"$ref", "description", "summary", "title", "examples", "example", "default", "$comment"}
            if set(value) - annotations:
                raise ValueError("validation siblings of $ref require explicit checker support; inline the schema instead")
        if "x-maxBytes" in value and (type(value["x-maxBytes"]) is not int or value["x-maxBytes"] < 1):
            raise ValueError("x-maxBytes must be a positive integer")
        if "x-trimSpace" in value and type(value["x-trimSpace"]) is not bool:
            raise ValueError("x-trimSpace must be boolean")
        for key, child in value.items():
            if key == "schema":
                Draft202012Validator.check_schema(child)
            walk(document, child)
    elif isinstance(value, list):
        for child in value:
            walk(document, child)

def load(path):
    document = yaml.safe_load(Path(path).read_text())
    official = json.loads((ROOT / "api/schema/openapi-3.1.json").read_text())
    Draft202012Validator(official).validate(document)
    walk(document, document)
    for schema in document.get("components", {}).get("schemas", {}).values():
        Draft202012Validator.check_schema(schema)
    return document

def validate(document, schema, value):
    # Components at this validation root preserve nested local schema targets.
    Validator({**schema, "components": document.get("components", {})}, format_checker=FORMATS).validate(value)

def sample_check(document, sample):
    operation = document["paths"][sample["route"]]["get" if sample["method"].lower() == "head" else sample["method"].lower()]
    if "request" in sample:
        schema = resolve(document, operation["requestBody"])["content"]["application/json"]["schema"]
        try:
            validate(document, schema, sample["request"])
        except ValidationError:
            if sample.get("request_valid", True):
                raise
        else:
            if not sample.get("request_valid", True):
                raise ValueError("an invalid request matched the declared schema")
    parameters = document["paths"][sample["route"]].get("parameters", []) + operation.get("parameters", [])
    for param in parameters:
        param = resolve(document, param)
        key = param["in"] + ":" + param["name"]
        if key in sample.get("parameters", {}):
            try:
                validate(document, param["schema"], sample["parameters"][key])
            except ValidationError:
                if key not in sample.get("invalid_parameters", []):
                    raise
            else:
                if key in sample.get("invalid_parameters", []):
                    raise ValueError("an invalid parameter matched the declared schema")
    responses = operation["responses"]
    response = resolve(document, responses.get(str(sample["status"]), responses.get("default")))
    if response is None:
        raise ValueError(f"undocumented status {sample['status']}")
    headers = {k.lower(): v for k, v in sample.get("headers", {}).items()}
    for name, header in response.get("headers", {}).items():
        header = resolve(document, header)
        if header.get("x-required") and name.lower() not in headers:
            raise ValueError("missing guaranteed response header: " + name)
        if name.lower() in headers:
            schema = resolve(document, header)["schema"]
            value = headers[name.lower()]
            if schema.get("type") == "integer":
                value = int(value)
            validate(document, schema, value)
    # HEAD, 204 and 304 correctly carry no entity, irrespective of GET schemas.
    if sample["method"].lower() == "head":
        return
    if sample["status"] in (204, 304, 412):
        if sample.get("body") not in (None, ""):
            raise ValueError("a bodyless response has an entity")
        return
    content = response.get("content", {})
    if content:
        media_type = headers.get("content-type", "").split(";", 1)[0]
        if media_type not in content:
            raise ValueError(f"unexpected response Content-Type {media_type}")
        validate(document, content[media_type]["schema"], sample.get("body"))

def expanded(document, schema):
    schema = resolve(document, schema)
    if isinstance(schema, dict):
        return {k: expanded(document, v) for k, v in schema.items()
                if k not in {"description", "examples", "example", "title", "default"}}
    if isinstance(schema, list):
        return [expanded(document, v) for v in schema]
    return schema

def snapshot(document):
    result = {}
    for route, path in document["paths"].items():
        for method, operation in path.items():
            if method not in METHODS:
                continue
            contract = {"parameters": {}}
            for parameter in path.get("parameters", []) + operation.get("parameters", []):
                parameter = resolve(document, parameter)
                contract["parameters"][parameter["in"] + ":" + parameter["name"]] = {
                    "required": parameter.get("required", False), "schema": expanded(document, parameter["schema"])}
            if "requestBody" in operation:
                body = resolve(document, operation["requestBody"])
                contract["request_required"] = body.get("required", False)
                contract["request"] = expanded(document, body.get("content", {}))
            contract["responses"] = {}
            contract["response_headers"] = {}
            for status, response in operation["responses"].items():
                response = resolve(document, response)
                contract["responses"][status] = expanded(document, response.get("content", {}))
                contract["response_headers"][status] = {}
                for name, header in response.get("headers", {}).items():
                    header = resolve(document, header)
                    contract["response_headers"][status][name.lower()] = {
                        "required": header.get("x-required", False), "schema": expanded(document, header["schema"])}
            result[method.upper() + " " + route] = contract
    return result

def compare_schema(old, new, direction, location, failures):
    if not isinstance(old, dict) or not isinstance(new, dict):
        if old != new:
            failures.append(location + ": schema shape changed")
        return
    handled = {"type", "properties", "required", "enum", "additionalProperties", "items", "oneOf", "anyOf", "allOf",
               "maxLength", "maxItems", "maximum", "x-maxBytes", "minLength", "minItems", "minimum"}
    for keyword in (set(old) | set(new)) - handled:
        if old.get(keyword) != new.get(keyword):
            failures.append(location + ": " + keyword + " changed; explicit review required")
    def types(schema):
        value = schema.get("type", [])
        return set(value if isinstance(value, list) else [value])
    a, b = types(old), types(new)
    if (a and b and not (a <= b if direction == "request" else b <= a)) or (direction == "request" and not a and b) or (direction == "response" and a and not b):
        failures.append(location + ": incompatible types")
    # Diagnostics are open vocabularies, including old specs that represented
    # the known examples as a closed enum contrary to the client guidance.
    is_code = location.endswith("/code") or location.endswith("/error_code")
    if not is_code and ((direction == "request" and "enum" not in old and "enum" in new) or (direction == "response" and "enum" in old and "enum" not in new)):
        failures.append(location + ": enum constraint changed incompatibly")
    if "enum" in old and "enum" in new and not is_code:
        a, b = set(old["enum"]), set(new["enum"])
        if not (a <= b if direction == "request" else b <= a):
            failures.append(location + ": incompatible enum")
    a, b = set(old.get("required", [])), set(new.get("required", []))
    if not (b <= a if direction == "request" else a <= b):
        failures.append(location + ": incompatible required fields")
    for field, child in old.get("properties", {}).items():
        if field not in new.get("properties", {}):
            failures.append(location + "/" + field + ": property removed")
        else:
            compare_schema(child, new["properties"][field], direction, location + "/" + field, failures)
    if direction == "request":
        for limit in ("maxLength", "maxItems", "maximum", "x-maxBytes"):
            if limit in new and ((limit in old and new[limit] < old[limit]) or (limit not in old and not (limit == "x-maxBytes" and old.get("maxLength") == new[limit]))):
                failures.append(location + ": " + limit + " narrowed")
        for limit in ("minLength", "minItems", "minimum"):
            if limit in new and (limit not in old or new[limit] > old[limit]):
                failures.append(location + ": " + limit + " narrowed")
        if old.get("additionalProperties", True) is not False and new.get("additionalProperties") is False:
            failures.append(location + ": additional properties rejected")
    else:
        for limit in ("maxLength", "maxItems", "maximum", "x-maxBytes"):
            if limit in old and (limit not in new or new[limit] > old[limit]):
                failures.append(location + ": response " + limit + " weakened")
        for limit in ("minLength", "minItems", "minimum"):
            if limit in old and (limit not in new or new[limit] < old[limit]):
                failures.append(location + ": response " + limit + " weakened")
    if isinstance(old.get("additionalProperties"), dict) or isinstance(new.get("additionalProperties"), dict):
        if old.get("additionalProperties") != new.get("additionalProperties"):
            failures.append(location + ": additional property schema changed; explicit review required")
    if "items" in old:
        if "items" not in new:
            failures.append(location + ": items removed")
        else:
            compare_schema(old["items"], new["items"], direction, location + "/items", failures)
    for keyword in ("oneOf", "anyOf", "allOf"):
        if keyword in new and keyword not in old:
            failures.append(location + ": " + keyword + " added; explicit review required")
        if keyword in old:
            if keyword not in new or len(old[keyword]) != len(new[keyword]):
                failures.append(location + ": " + keyword + " variants changed; review required")
            else:
                for i, child in enumerate(old[keyword]):
                    compare_schema(child, new[keyword][i], direction, f"{location}/{keyword}/{i}", failures)

def compatibility(baseline, current):
    failures = []
    for operation, old in baseline.items():
        if operation not in current:
            failures.append(operation + ": operation removed")
            continue
        new = current[operation]
        for key, param in old["parameters"].items():
            if key not in new["parameters"]:
                failures.append(operation + ": parameter removed: " + key)
            else:
                compare_schema(param["schema"], new["parameters"][key]["schema"], "request", operation + "/" + key, failures)
        for key, param in new["parameters"].items():
            if param["required"] and not old["parameters"].get(key, {}).get("required", False):
                failures.append(operation + ": required parameter added: " + key)
        if new.get("request_required") and not old.get("request_required"):
            failures.append(operation + ": required request body added")
        for media, entry in old.get("request", {}).items():
            if media not in new.get("request", {}):
                failures.append(operation + ": request media type removed")
            else:
                compare_schema(entry["schema"], new["request"][media]["schema"], "request", operation + "/request", failures)
        for status, content in old["responses"].items():
            if status not in new["responses"]:
                failures.append(operation + ": response removed: " + status)
                continue
            for media, entry in content.items():
                if media not in new["responses"][status]:
                    failures.append(operation + ": response media type removed: " + status)
                else:
                    compare_schema(entry["schema"], new["responses"][status][media]["schema"],
                                   "response", operation + "/response/" + status, failures)
        for status, headers in old.get("response_headers", {}).items():
            for name, header in headers.items():
                current_header = new.get("response_headers", {}).get(status, {}).get(name)
                if current_header is None:
                    failures.append(operation + ": response header removed: " + name)
                else:
                    if header["required"] and not current_header["required"]:
                        failures.append(operation + ": required response header weakened: " + name)
                    compare_schema(header["schema"], current_header["schema"], "response", operation + "/header/" + name, failures)
    return failures

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--spec", type=Path, default=ROOT / "api/openapi.yaml")
    parser.add_argument("--samples", type=Path)
    parser.add_argument("--baseline", type=Path, default=ROOT / "api/compatibility-v0.2.1.json")
    parser.add_argument("--write-baseline", type=Path)
    args = parser.parse_args()
    document = load(args.spec)
    if args.write_baseline:
        args.write_baseline.write_text(json.dumps(snapshot(document), indent=2) + "\n")
        return
    failures = compatibility(json.loads(args.baseline.read_text()), snapshot(document))
    if failures:
        raise ValueError("incompatible public contract changes:\n" + "\n".join(failures))
    samples = json.loads(args.samples.read_text()) if args.samples else []
    for sample in samples:
        try:
            sample_check(document, sample)
        except (ValidationError, ValueError, KeyError) as error:
            raise ValueError(f"fixture {sample['name']}: {error}") from error
    print(f"OpenAPI, local references, compatibility and {len(samples)} runtime samples passed")

if __name__ == "__main__":
    try:
        main()
    except (ValidationError, ValueError, KeyError) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
