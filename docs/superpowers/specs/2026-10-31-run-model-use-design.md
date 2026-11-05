# Every run says what it spent on the model

**Why.** The owner wants to see, per sign-in run, how many model calls it made, which model, and the
total price.

- **The worker reports, the engine prices.** Each `run_flow` and `run_check` reply carries `cost`: calls,
  model, tokens in, cached, written to the cache, out. A run adds up every step it played (a flow, and
  each check). browser-use's own pricing stays off, since it fetches its table over the network.
- **Priced when it reports**, from `internal/signins/pricing.go`, and stored: a later price change does
  not rewrite old runs. These are list prices, an estimate of the bill, not the invoice.
- **Never a false $0.** A model with no known price, or calls with no tokens counted, is "price unknown".
  A step that died before reporting makes the count "at least". A run from before this has no row
  (`run_usage`) and says nothing.
- **Shown** in the site's Runs list, and on the run's page with its tokens.
