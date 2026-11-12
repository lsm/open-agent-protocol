# DeepSeek corpus provenance

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

The DeepSeek corpus records, per case, the upstream commit each recording
was made at and the git blob hashes of the source files that produced it.
The offline corpus test only checks a case against the three known
provenance maps in `go/adapter/deepseek/corpus_test.go`, so a sweep that
rewrites a map together with the cases stays self-consistent and green. The
opt-in check anchors the maps to upstream instead:

```sh
OAP_DSH_PROVENANCE_CHECK=1 go test ./go/adapter/deepseek/ \
  -run TestCorpusProvenanceSourcesExistInTheirDeclaredCommits -count=1
```

It fetches each declared commit's tree from the public GitHub API, without
credentials, and fails naming any recorded blob that is absent from that
tree. Run it after editing corpus provenance, after moving the DeepSeek pin,
and after adding a carried-forward case. To see it bite, point a blob in one
of the three source maps at another release's value: the check must name the
blob and the commit it does not belong to.
