# Документация / Documentation

[Русский README](../README.md) · [English README](../README.en.md)

Выберите способ использования в README, затем переходите к нужному руководству. Основной README доступен на русском и английском; подробные технические руководства ниже написаны на английском.

Choose an interface in the README, then follow the relevant guide. The project overview is available in Russian and English; detailed technical guides below are in English.

## Локальная нарезка / Local cutting

| Guide | Covers |
| --- | --- |
| [CLI and Go library](usage.md) | Installation, inspect/export, errors, local limits and a complete HTTP example |
| [Plans, preflight and joined outputs](batch-cutting.md) | Saved JSON plans, checking before execution, output order, audio and subtitles |
| [Editable clip collections](clip-collections.md) | Independent clips, ordered fragments, draft/final exports, crop and selected IDs |
| [Agent workflow](agent-workflow.md) | A reproducible interview-to-clips scenario and safe revisions/retries |
| [Python and TypeScript SDKs](sdk.md) | Source installation, local/API workflows, waiting, recovery and downloads |
| [Resumable SDK collections](sdk-collections.md) | Complete projects, process interruption, downloads and selected revisions |
| [Python reference](../sdk/python/README.md) | Python client options and typed results |
| [TypeScript reference](../sdk/typescript/README.md) | HTTP/browser and Node local entry points |
| [Media policy](media.md) | Streams, display geometry, timing, color and quality boundaries |
| [Platforms](platforms.md) | Tested OS/architecture combinations and native acceptance |

## Сервер и эксплуатация

<a id="hosting-and-operations"></a>

| Guide | Covers |
| --- | --- |
| [HTTP API](api.md) · [OpenAPI](../api/openapi.yaml) | Sessions, sources, jobs, collections, preflight, errors and artifact downloads |
| [Public self-hosting](self-hosting.md) | Linux HTTPS installation and required acceptance |
| [Configuration](configuration.md) | Environment settings, concurrency, timeouts and quotas |
| [Operations](operations.md) | HTTPS, maintenance, deployment, upgrade and recovery |
| [Backup and recovery](backup-recovery.md) | Coordinated database/media snapshots and restoration |
| [Providers](providers.md) | Recognized sources, verified integrations and unsupported media |
| [Security model](security-model.md) | Ownership, URL guards, sessions and resource boundaries |

## Архитектура и хранение / Architecture and storage

| Guide | Covers |
| --- | --- |
| [Architecture and repository map](architecture.md) | Engine, CLI, server, SDKs, languages and runtime dependencies |
| [Database migrations](migrations.md) | Ordered SQL history, checksums and startup compatibility |
| [Storage capacity](storage-capacity.md) | Admission, physical headroom, scan progress and workload evidence |
| [Source retention](source-retention.md) | Pin protection, expiration and drift recovery |
| [Successful-export media cleanup](source-media-cleanup-upgrade.md) | Migration 6 lifecycle, upgrade and compatible recovery |
| [Source-retention transition](self-hosting-source-retention-transition.md) | Migration 5 public-hosting upgrade/restore acceptance |

## Разработка и выпуски / Development and releases

| Guide | Covers |
| --- | --- |
| [Contributing](../CONTRIBUTING.md) | Environment, focused checks, SDK acceptance and pull requests |
| [Changelog](../CHANGELOG.md) | Published versions and unreleased features |
| [CLI releases](releases.md) | Release-page conventions, reproducible packaging and consumer signature verification |
| [1.4.0 release candidate](release-1.4.0.md) | Resumable SDK scope, verification and publication boundaries |
| [Performance](performance.md) | Matched measurements, retained results and limits of speed claims |
| [Artifact retention](artifacts.md) | Keeping large diagnostics outside the source tree |
| [Security reporting](../SECURITY.md) | Private vulnerability reports |
| [Dependency notices](../THIRD_PARTY_NOTICES.md) | Third-party licenses and module identities |
