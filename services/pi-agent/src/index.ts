import { loadConfig } from "./config.js";
import { PiRuntime } from "./pi-runtime.js";
import { createApp } from "./server.js";

const config = await loadConfig();
const runtime = new PiRuntime(config);
const { server } = createApp(config, runtime);

server.listen(config.port, config.host, () => {
  console.log(`pi-agent listening on ${config.host}:${config.port}`);
});

async function shutdown(): Promise<void> {
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(1), 10_000).unref();
}

process.on("SIGTERM", () => void shutdown());
process.on("SIGINT", () => void shutdown());
