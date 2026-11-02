# Supplier read recovery and homepage release

Includes the 2026-11-02 handoff patches: persist authoritative expiry, retry credential reads once, encrypted credential cache, lifecycle refresh during admin sync, and detail ownership version. Delivered-order completion helper was already in main and is retained.

Cache fallback and live results share a fresh ownership/provider/binding check and mandatory credential-view audit. No password is included in audit or error logging. Cache writes use compare-and-swap of the prior snapshot, preserving concurrent edits. AES-GCM uses the existing snapshot key; missing/wrong keys fail closed. Cached default port semantics are preserved. No new schema migration.

The frontend release includes shared marketing header/footer and homepage. The industry reference and bottom resource CTA remain unpublished. Backend update policy permits only named, hashed marketing MP4 assets. Install this backend before the companion frontend; production installation remains manual. No real purchase, renewal, charge, machine write or upstream mutation is used for validation.
