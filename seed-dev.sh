#!/bin/sh
# Seeds a running dev server with prompts and real commit history.
# `put` writes content only — `publish` is what creates commits, which is what
# the UI's history/diff views read. Usage: sh seed-dev.sh [addr]
set -e
ADDR=${1:-localhost:8443}
P="./priompt.exe publish -addr $ADDR"

$P -uri priompt://acme/onboarding/welcome -file - -slot name -slot org <<'EOF'
Hi {name}, welcome to {org}! Your workspace is ready.
EOF
$P -uri priompt://acme/onboarding/welcome -file - -slot name -slot org <<'EOF'
Hi {name}, welcome to {org}! Your workspace is ready to go.
EOF
$P -uri priompt://acme/onboarding/welcome -file - -slot name -slot org <<'EOF'
Hi {name}, welcome aboard at {org}. Your workspace is ready to go.
Ping us any time if something looks off.
EOF

$P -uri priompt://acme/onboarding/reminder -file - -slot name -slot count <<'EOF'
Hey {name}, you have {count} steps left in setup. Finish up?
EOF

$P -uri priompt://acme/support/triage -file - -slot categories -slot ticket <<'EOF'
You are a support triage agent. Classify the ticket into one of {categories}.

Ticket: {ticket}
EOF
$P -uri priompt://acme/support/triage -file - -slot categories -slot ticket <<'EOF'
You are a support triage agent. Read the ticket carefully and classify it into
exactly one of {categories}. If you are unsure, pick the closest match and say so.

Ticket: {ticket}
EOF

$P -uri priompt://acme/support/escalate -file - -slot ticket -slot team <<'EOF'
Escalate to a human. Summarize {ticket} for {team} in under 100 words.
EOF

$P -uri priompt://acme/agents/planner -file - -slot agent -slot goal <<'EOF'
You are {agent}, a planning agent. Break {goal} into ordered, verifiable steps.
EOF

$P -uri priompt://acme/system-prompt -file - -slot date <<'EOF'
You are a careful assistant. Today is {date}. Answer concisely.
EOF

$P -uri priompt://acme/content/summarize -file - -slot audience -slot thread <<'EOF'
Summarize this thread for {audience}: {thread}
EOF

./priompt.exe list -addr "$ADDR"
