# Contributing

We welcome contributions and are happy to discuss ideas, answer questions,
and help you get started.

Before opening a pull request, please open an issue or discussion with the
maintainers. This helps align the change with the project's direction and
avoids duplicated work.

## Development

The project requires Go 1.26 or newer.

```sh
make ci
```

This checks formatting, runs `go vet`, executes the full test suite, and builds
the CLI. Pull request titles must follow Conventional Commits, for example
`feat: add warehouse validation` or `fix: preserve exported SQL whitespace`.
