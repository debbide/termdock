# Fixed Tunnel

Set `cloudflare.mode` to `fixed` and store the tunnel token in the configured token file. The token file must be owned appropriately and have mode `0600` or stricter.

The token is read from the file and is never written to application logs or status output. Cloudflare Access can be applied in front of the tunnel as an additional authentication layer.
