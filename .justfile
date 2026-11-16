# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::default do::lint::go::default
fix: do::fix::default do::fix::go::default
test: do::test::go::unit do::test::go::race do::test::go::cover
security: do::security::default do::security::go::default

# The command, built from this module at the commit checked out:
# `just sonos --host 192.0.2.10 rooms`.

# Positional, so a room's name with a space in it reaches the command as one
# argument.
[doc('Build sonos and run it: `just sonos --host 192.0.2.10 rooms`.')]
[positional-arguments]
sonos +args: build-sonos
    .build/sonos "$@"

build-sonos:
    mkdir -p .build
    go build -o .build/sonos ./cmd/sonos
