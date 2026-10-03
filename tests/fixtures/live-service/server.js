// Serves message.txt on the loopback, read per request, so a check can see
// edits reach a running service and the service move back to the laptop.
const http = require("http");
const fs = require("fs");
const path = require("path");
const port = Number(process.env.PORT || 47900);
http.createServer((req, res) => {
  res.end(fs.readFileSync(path.join(__dirname, "message.txt"), "utf8").trim() + " from " + process.platform);
}).listen(port, "127.0.0.1");
