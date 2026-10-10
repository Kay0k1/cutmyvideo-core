# Historical migration fixtures

These SQL files were captured from repository history, independently of the
current migration runner. Tests populate them with synthetic source, queue,
artifact and storage data before upgrading.

| Fixture | Source |
| --- | --- |
| `unversioned-initial.sql` | `schema` in `b7923f1:internal/app/store.go` |
| `unversioned-provider-id.sql` | `schema` in `c287638:internal/app/store.go` |
| `unversioned-platform.sql` | `schema` in `d0f30f2:internal/app/store.go` |
| `unversioned-metadata.sql` | `schema` and `platformMetadataCacheSchema` in commit `109c64e` |
| `v0.2.1.sql` | The exact three application schema constants from tag v0.2.1 (`f2be62c42a6a0d3d7a017b40bd683596bc578b95`), plus its historical version-table creation and marker insert |

Keep released/historical fixtures immutable. Do not replace them with the
current schema to make upgrade tests pass.
