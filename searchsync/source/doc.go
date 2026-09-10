/*
Package syncsource adapts a repository to the two read seams searchsync
defines: searchsync.Fetcher, which the change feed reads one document per event
through, and searchsync.Scanner, which a reindex walks the whole source with.

An application indexing nine entities has nine of each, and they differ in three
functions: how to read one row, how to page over IDs, and how to turn a row into
the subset that gets indexed. Everything else — omitting rows that have since
been deleted, holding the byte ordering a reindex depends on, naming the index in
the error, wrapping a row in a Document — is the same work every time, and the
version of it written out per entity is the version where one of the nine quietly
disagrees with the other eight.

	source, err := syncsource.New("orders",
	    repo.GetOrder,               // func(ctx, id) (*Order, error)
	    repo.ScanOrderIDsForReindex, // func(ctx, after, limit) ([]string, error)
	    convertOrderToSearchSubset,  // func(*Order) *OrderSearchSubset
	)
	if err != nil {
	    return err
	}

	syncer, err := syncsource.NewSyncer(source, index, syncsource.WithPillars(pillars))
	if err != nil {
	    return err
	}

	reindexer, err := syncsource.NewReindexer(source, index, syncsource.WithPillars(pillars))

The three functions are the repository's, and stay there: FetchFunc is its
existing get-by-ID and ScanFunc is its keyset walk over IDs. This package
supplies neither, and shouldn't — it does not know what a row is or how to read
one.

# Scan is implemented in terms of Fetch

Fetcher and Scanner have a correctness relationship their interfaces state
nowhere: both must produce the same document for the same row. Where they don't,
a reindex overwrites what the change feed wrote with a differently-shaped copy,
and the index holds two generations of schema at once with nothing to detect it.
Nothing about implementing the two separately, nine times, makes that
relationship visible, and the failure it produces is silent.

So there is one transform here, not two, and the cheapest way to guarantee
agreement is to have one seam call the other: the scan query names the next page
of IDs and the fetch turns them into documents. It costs a second round trip per
page, on the background path where that is affordable, and it removes the
possibility of two row-to-document transforms drifting apart.

# The three things a from-scratch implementation gets wrong

sql.ErrNoRows from the fetch is an expected outcome rather than a failure — the
row was deleted between the event being written and the event being handled — and
must be omitted from the batch rather than failing it. Failing it instead retries
the event until it dead-letters, with the stale document sitting in the index the
whole time. Fetch omits it.

A page shortened by that omission does not end the walk. searchsync.Scanner reads
a page shorter than limit as the end of the stream, so a Scan that dropped one
vanished row out of a full page would stop a reindex partway through and report
success. Scan asks for more IDs until it has a full page or the ID stream is
genuinely exhausted.

The IDs a reindex walks must ascend in byte order, as Go's < compares strings,
and that is checked here rather than repaired. Postgres's default en_US.UTF-8
collation sorts case-insensitively and ignores punctuation, which is a different
order; a keyset walk over it wants ORDER BY id COLLATE "C". Sorting each page
instead would fix what the page looks like and nothing else — the query is what
resumes, so a locale-collated walk still skips every row between one page's
largest ID and the next page's first, and arrives downstream in perfect order
while doing it. A pruning reindex then deletes those live documents. Scan checks
what the ScanFunc returned and fails with searchsync.ErrUnsortedScan.

# Text indexes only

NewSyncer and NewReindexer build against textsearch.IndexManager because a
ConvertFunc produces a body and not an embedding, and a vector target refuses a
document without one. An application indexing vectors builds
searchsync.VectorTarget itself and supplies documents that carry embeddings; that
is a different transform, not this one with a field added.
*/
package syncsource
