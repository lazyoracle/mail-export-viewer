// Package index keeps the persistent SQLite (pure Go, modernc.org/sqlite)
// and FTS5 index of every message of every Source: metadata, locators to
// re-read bodies lazily, labels/categories/tags, flags, properties and
// threads. It consumes source.Source generically and never imports a
// format package.
package index
