# Recoverable agent project acceptance

This record covers installed Python and Node packages, the real HTTP API,
PostgreSQL, the worker and an actual browser. The implementation snapshot is
`ae99fce692f37b701d0be83c2131c11e78d1230e`, tree
`0c6c87e6cc8571fdec688e32aa6b932dc4817f14`. The SDK build materialized that
clean Git snapshot, installed its wheel/source/npm distributions, ran every
required consumer and sealed the exact tested files without rebuilding them.
[The bounded summary](summary.json) records source, package, harness and content
verification digests. This is immutable SDK6 candidate evidence, separate from final
main/tag checks, actual release signatures and production deployment.
The earlier implementation record remains unchanged as
[initial-summary.json](initial-summary.json), bound to source
`9cdf291910064f04ebba91d223a3f9df9cbdeff2`. Its whole-source race and
vulnerability-check log belongs to that initial snapshot; it is not a
whole-source SDK6 result. Windows helper follow-up and fixture cleanup are
separate reviewed changes, covered by the new installed-package checks.

The exact sealed and installed SDK distributions are:

| Kind | Package | SHA256 |
| --- | --- | --- |
| npm | cutmyvideo-sdk-1.4.0.tgz | bb6745390627f57b6177bd56a56a1119425c4fa57871a702278d60a3f426412c |
| sdist | cutmyvideo-1.4.0.tar.gz | 825262b98fdc1fda92d95e97fc865d795de8a601e9afdff32c4c5c7946c74a0a |
| wheel | cutmyvideo-1.4.0-py3-none-any.whl | 683de32ef9e472a1b60c9b831ea0dfba06a93ec8e45c134c7f941e2fd3738b03 |

## Interview and workflow

The complete 516.912472-second, 1280×720 recording is Wikimedia Deutschland's
[Interview WMF.ogv](https://commons.wikimedia.org/wiki/File:Interview_WMF.ogv),
[CC BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0). Its normalization
and original-file identity are retained in the
[earlier fixture record](../2026-10-15-sdk-collections/README.md).
The frozen MP4 contains 82,242,740 bytes, SHA256
`0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca`.

Each language runs independently with one source and fifteen stable clip IDs.
Each clip joins a six-second and a three-second passage, alternating their
order; clip-03 uses a half-width crop. Actual uploaded bytes are compared with
the frozen source. The new project scenario requires:

- A committed upload loses its response. The harness stops the SDK process
  through the operating system; a fresh process restores its private owner,
  exact intent and receipt, producing one source.
- The original clip-15 request encounters a real worker deadline failure. The
  other fourteen clips finish normally. An explicit repair loses its committed
  response, then another OS stop and fresh resume recover the same attempt.
  There are exactly sixteen jobs and fifteen artifacts at this point.
- A separate browser session redeems a short-lived project handoff. Ordinary
  recipient routes remain isolated; the URL fragment is removed. The browser
  changes clip-01's first boundary by 40ms and clip-15's crop, exports those two
  clips, then saves all fifteen selected IDs without exporting again.
- The SDK adopts the reviewed project and existing jobs. Exactly eighteen jobs
  and seventeen artifacts exist, including the failed original attempt and two
  reviewed exports. Thirteen original completed job IDs, file hashes and paths
  are retained. Replaying the final collection creates no further work.
- Fifteen supplied SRT sidecars are derived from actual exported segment maps.
  Cue bounds, rebasing, text, counts and hashes are checked independently in the
  harness. The supplied Unicode cue is a timing fixture; it is not a transcript
  and makes no speech-word accuracy claim.

Both real browser receipts and screenshots remain in private evidence storage;
only aggregate observations and digests are committed. No owner cookie, handoff
token, source authority, private journal, video or audio is a release asset.

## Independent content and retained recovery

The [project content verifier](../../scripts/sdk-agent-project-content-verify.py)
decodes the original source and reviewed clips using the caller's reviewed
requests as its reference, rather than trusting SDK maps. Each SDK passes sixty
frame comparisons and thirty speech comparisons. Maximum RGB mean error is
0.4187 against limit12; minimum audio correlation is 0.8894 against minimum0.75.
The sidecar file digests and browser review receipts are checked again.

The earlier durable collection scenario also runs against these newly installed
packages: lost committed submission, a genuinely interrupted artifact transfer,
saved verified prefix, OS stop, exact ranged continuation, a second OS stop at
eight ready files, fresh resume, replay, two revised finals and two selected
drafts. Returning to all finals adds no work: fifteen initial outputs, seventeen
after revision and nineteen after drafts. Both SDKs preserve the actual server
admission delay; this artificial queue control is not a speed benchmark.
Its [independent content verifier](../../scripts/sdk-collection-acceptance-verify.py)
checks all nineteen unique artifacts per language, including geometry, ordered
passages and selected audio. All files also match backend-sealed bytes and
fully decode in the required installed-package harness.

The strict package receipt requires all five parent tests and both language
subtests of each, with no missing, failed or skipped consumers. Python64 unit
tests pass against the installed wheel; TypeScript63 pass against both source
and installed distributions. The source distribution rebuilds to the same
Python module payload as the installed wheel.
The source snapshot and SDK/acceptance inputs are hashed before and after the
run; all required tests and three distribution hashes enter the sealed SDK
manifest. Subsequent documentation, test or release-gate changes receive their
own final-source CI observations and cannot borrow this snapshot's commit.

## Reproduce and release boundary

Use the pinned tools and a dedicated disposable database from
[CONTRIBUTING](../../CONTRIBUTING.md). Supply the exact interview through
`CUTMY_SDK_COLLECTION_INTERVIEW`; set `CUTMY_AGENT_PROJECT_WEB_ASSETS` to the
companion website's built assets and `CUTMY_AGENT_PROJECT_BROWSER_DRIVER` to its
`e2e/agent-project-review-consumer.mjs`. Evidence directories are explicit private
paths outside both repositories. Run
[the immutable SDK build](../../scripts/release-sdk.py) with the reviewed
`SOURCE.json` and its independent SHA256. The command installs packages, runs
all fifteen required tests and seals the exact distributions. Run the two
independent content verifiers against the retained original requests, receipts
and files; unavailable tools or mismatched content fail.

Schema8 compatibility and coordinated database/media restoration have separate
owned-PostgreSQL tests. Public HTTPS hosting must pass the explicit
`agent-projects-007-to-008` mode; historical modes retain their reviewed pins.
The companion site's activation drill uses actual schema7/8 binaries and catalog
shapes to verify coordinated restoration and retained media in persistent
volumes. These are distinct from the interview test.

Final publication requires the exact main/tag source gates, native media and
installed SDK consumers on all five platforms, extracted CLI acceptance,
separate four-subject SDK attestation, and authenticated downloaded-package
installation on all five targets. Candidate packages in this record are not
signed published assets. Follow [the release protocol](../../docs/releases.md)
for the actual eighteen-asset release and its independent final-source checks.
