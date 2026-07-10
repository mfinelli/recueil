// Recueil Worker — phase 0 stub.
//
// No real logic yet. This exists so Terraform has a script to deploy and
// bind D1 / R2 / the service secret to, so later phases can build directly
// against env.DB, env.BUCKET, and env.SERVICE_SECRET without a second
// infrastructure change. See the design doc §2/§11 for what this Worker
// will eventually do: device auth, queue enqueue, presigned R2 URLs, D1
// read/write, and the service-secret-gated backend endpoints.

export default {
  async fetch(request, env, ctx) {
    return new Response("Recueil Worker: not yet implemented", {
      status: 501,
    });
  },
};
