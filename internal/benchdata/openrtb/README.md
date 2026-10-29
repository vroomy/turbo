# OpenRTB benchmark fixtures

Adapted from IAB Tech Lab's [OpenRTB 2.6 examples](https://github.com/InteractiveAdvertisingBureau/openrtb2.x/blob/main/2.6.md#6-bid-requestresponse-samples):

- Section 6.2.1, Simple Banner request.
- Section 6.3.1, Ad Served on Win Notice response.

The response auction ID, impression ID and win-notice impression parameter are
changed to match the request. JSON whitespace is reduced for readability. These
are a banner bid and its win-notice response; sample URLs are payload strings.
The specification is licensed by IAB Tech Lab under
[Creative Commons Attribution 3.0](https://creativecommons.org/licenses/by/3.0/).

The HTTP benchmark sends these exact UTF-8 bytes, including the final newline,
using POST /openrtb2/auction and application/json. Both servers consume the full
request body and return the prebuilt response with explicit Content-Length and
Content-Type. JSON validation runs outside timing. The workload measures HTTP,
body transfer and cleanup; it does not decode/encode JSON or run an auction.
