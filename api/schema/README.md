# OpenAPI validation schema

The unchanged OpenAPI Initiative structural schema is from
<https://spec.openapis.org/oas/3.1/schema/2025-09-15>. Its [Apache 2.0 license](LICENSE)
is included. Validation is offline; updating this file requires reviewing the
upstream schema and retaining its license.

The structural schema validates the OpenAPI document. The contract checker also
validates its JSON Schema objects, resolves local references and validates real
HTTP fixtures against request/response schemas. The x-maxBytes extension means a
UTF-8 byte bound; standard JSON Schema maxLength counts characters.
