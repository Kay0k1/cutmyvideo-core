package coremodel

import "errors"

// ErrSchemaIncompatible preserves the service startup refusal identity without
// making local consumers depend on schema or database implementation packages.
var ErrSchemaIncompatible = errors.New("database schema is incompatible with this binary")

// ErrStorageInitializing preserves the retryable accounting bootstrap identity.
var ErrStorageInitializing = errors.New("storage accounting is initializing")
