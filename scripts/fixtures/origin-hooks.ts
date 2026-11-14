// A third-party input handler for check-origin-pi.mjs, loaded after yesman's
// extension: it handles one input and rewrites another, as other pi
// extensions may. Neither must be affected by yesman's origin tracking.
export default function (pi) {
  pi.on("input", (event) => {
    if (event.text === "fixture-ping") return { action: "handled" };
    if (event.text === "fixture-transform") return { action: "transform", text: "transformed by a hook" };
  });
}
