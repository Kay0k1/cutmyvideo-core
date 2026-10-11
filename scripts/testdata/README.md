# Immutable metadata for pure self-hosting controls

These JSON files contain Git file metadata, not runnable source or acceptance
results. Pure controls use them to model fixed predecessors and historical
candidates independently of later application changes. No hosted, PostgreSQL,
media, performance or deployment evidence follows from a passing fixture test.

- `selfhosting-old-application.json` retains the original 164-file application
  map of `0f93cd654a998be7740ba1d6fee4a44a6f7b3464`, matching the reviewed reference
  `06bb74bca90a79fbf399c01cf71f7f44af186431`. Its canonical SHA-256 remains
  `a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90`.
- `selfhosting-candidate-application.json` freezes the 173-file application map
  of `732340c43a71b332da31b3580c3c5048e936a000`, tree
  `23c85879642f33a896d25eb795dfab91f3af0d9c`. Its canonical SHA-256 is
  `d1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6`.
- `selfhosting-current-baseline-source.json` freezes the complete 463-file source
  inventory of the unchanged-schema 005 predecessor
  `1d684b4b0918138190439fc4ce223ff1bc22de40`, tree
  `c568f55433cd2be92e9e5dffc956e1f1dca6cadb`. Controls reconstruct that exact Git
  tree from its modes, paths and blob identities. Its 173 application files have
  the same canonical digest as the historical candidate above.

Metadata was read from the immutable Git objects. Historical maps and verifier
pins must not be regenerated from a future working tree. Hosted acceptance
captures fresh raw `SOURCE.json` receipts from actual built images, including
their exact checkout/provider context and independently verified cleanup.
