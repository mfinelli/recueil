# recueil

## install

Step 1: create the cloudflare infrastructure using terraform: see the README in
the terraform directory.

## development

This repo uses a git submodule (`internal/urlnorm/clearurls-rules`, a pinned
snapshot of the [ClearURLs ruleset](https://github.com/ClearURLs/Rules) used by
`internal/urlnorm` for URL normalization) embedded directly into the Go binary
at build time. Clone with submodules, or initialize them afterward:

```sh
git clone --recurse-submodules https://github.com/mfinelli/recueil.git
# or, if already cloned:
git submodule update --init
```

The Go build (and `go:embed` specifically) will fail without this checked out.
To pull in a newer ruleset snapshot later:
`cd internal/urlnorm/clearurls-rules && git pull origin master` (or pin to a
specific commit/tag), then commit the resulting submodule pointer change as its
own commit.
