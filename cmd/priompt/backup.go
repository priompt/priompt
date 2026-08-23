package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	store "priomptdb"
)

// A backup carries the whole store: the served HEAD *and* the history behind it.
//
// The narrow pair — export/import — covers the prompts table alone. That is a
// fine way to move content between servers, and a poor way to protect it: a
// database rebuilt from it serves every prompt correctly and has no commits, no
// branches, and no ref for main to point at. History, branching and rollback —
// the whole reason this product exists rather than a key-value store — are gone,
// and nothing looks wrong, because the prompts still resolve.
//
// Splitting the two is the settled answer elsewhere: Dolt separates `dump` from
// `backup`, Postgres separates pg_dump from pg_basebackup plus WAL archiving,
// and both document that the logical export cannot reconstruct history. The
// mistake this fixes was not the narrow dump — it was calling it "backup".

// backupVersion is the format version, so a future reader can tell what it has.
const backupVersion = 1

// record is one line of a backup file. A tagged union keeps the format
// streamable — arbitrarily large stores never have to be held in memory at
// either end — and self-describing, so a reader can tell a full backup from a
// legacy served-HEAD dump without being told.
type record struct {
	Kind string `json:"kind"`

	// kind == "header"
	Version       int    `json:"version,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"`

	// kind == "prompt"
	Prompt *store.Prompt `json:"prompt,omitempty"`
	// kind == "commit"
	Commit *store.Commit `json:"commit,omitempty"`
	// kind == "ref"
	Ref *store.Ref `json:"ref,omitempty"`
}

func backup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "priompt.db", "sqlite file path, or a postgres:// DSN")
	out := fs.String("out", "-", "output file (- for stdout)")
	fs.Parse(args)

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	prompts, err := st.Dump(ctx)
	if err != nil {
		log.Fatal(err)
	}
	commits, err := st.DumpCommits(ctx)
	if err != nil {
		log.Fatal(err)
	}
	refs, err := st.DumpRefs(ctx)
	if err != nil {
		log.Fatal(err)
	}

	w := io.Writer(os.Stdout)
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	write := func(r record) {
		if err := enc.Encode(r); err != nil {
			log.Fatal(err)
		}
	}

	write(record{
		Kind: "header", Version: backupVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), SchemaVersion: st.SchemaVersion(),
	})
	// Commits first, then refs: a ref names a commit, so this order lets a
	// restore validate as it goes rather than buffering the whole file.
	for i := range commits {
		write(record{Kind: "commit", Commit: &commits[i]})
	}
	for i := range refs {
		write(record{Kind: "ref", Ref: &refs[i]})
	}
	for i := range prompts {
		write(record{Kind: "prompt", Prompt: &prompts[i]})
	}
	fmt.Fprintf(os.Stderr, "backed up %d prompts, %d commits, %d branch pointers\n",
		len(prompts), len(commits), len(refs))
}

func restore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dbPath := fs.String("db", "priompt.db", "sqlite file path, or a postgres:// DSN")
	in := fs.String("in", "-", "input file (- for stdin)")
	force := fs.Bool("force", false, "restore even though the target already has history")
	fs.Parse(args)

	r := io.Reader(os.Stdin)
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		r = f
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// Restoring into a store that already holds commits merges two histories,
	// and nothing here can tell whether that is a resumed restore or a mistake
	// aimed at a live database. Refuse by default: an operator who means it can
	// say so, and one who does not keeps their history.
	existing, err := st.CountCommits(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if existing > 0 && !*force {
		log.Fatalf("refusing to restore: %s already holds %d commits.\n"+
			"Restore into an empty database, or pass -force to merge this backup into the existing history.",
			*dbPath, existing)
	}

	var nP, nC, nR, legacy int
	dec := json.NewDecoder(r)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			log.Fatal(err)
		}
		var rec record
		if err := json.Unmarshal(raw, &rec); err != nil {
			log.Fatal(err)
		}

		switch rec.Kind {
		case "header":
			if rec.Version > backupVersion {
				log.Fatalf("backup format version %d is newer than this binary understands (%d)",
					rec.Version, backupVersion)
			}
		case "commit":
			if rec.Commit != nil {
				if err := st.PutCommit(ctx, *rec.Commit); err != nil {
					log.Fatal(err)
				}
				nC++
			}
		case "ref":
			if rec.Ref != nil {
				if err := st.PutRef(ctx, *rec.Ref); err != nil {
					log.Fatal(err)
				}
				nR++
			}
		case "prompt":
			if rec.Prompt != nil {
				if err := st.Put(ctx, *rec.Prompt); err != nil {
					log.Fatal(err)
				}
				nP++
			}
		case "":
			// A file written before backups carried history: bare prompt objects
			// with no kind tag. Still readable, so old snapshots are not stranded
			// — but say plainly that it has no history to give back.
			var p store.Prompt
			if err := json.Unmarshal(raw, &p); err != nil || p.URI == "" {
				log.Fatalf("unrecognised line in backup file: %s", trunc(string(raw)))
			}
			if err := st.Put(ctx, p); err != nil {
				log.Fatal(err)
			}
			nP++
			legacy++
		default:
			log.Fatalf("unknown record kind %q in backup file", rec.Kind)
		}
	}

	fmt.Printf("restored %d prompts, %d commits, %d branch pointers\n", nP, nC, nR)
	if legacy > 0 {
		fmt.Fprintf(os.Stderr,
			"note: this file is a served-content export (pre-history format), so no commits "+
				"or branches were restored. Prompts resolve, but history, branching and rollback "+
				"are unavailable for them.\n")
	}
}

func trunc(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
