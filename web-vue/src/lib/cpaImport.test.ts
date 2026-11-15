import { describe, expect, it } from "vite-plus/test";
import { parseCPAAccounts, readCPAFiles } from "./cpaImport";

describe("CPA import transport", () => {
  it("keeps provider identity and rotated OAuth fields for server validation", () => {
    const credential = {
      type: "codex",
      access_token: "test",
      refresh_token: "rt",
      id_token: "id",
      account_id: "account",
    };
    expect(parseCPAAccounts(JSON.stringify({ accounts: [credential] }), "fixture")).toEqual([
      credential,
    ]);
  });
  it("does not turn a foreign credential into an untyped access token", () => {
    expect(parseCPAAccounts('{"type":"claude","access_token":"test"}', "fixture")[0].type).toBe(
      "claude",
    );
  });
  it("rejects empty records", () => {
    expect(() => parseCPAAccounts("[]", "fixture")).toThrow("没有账号记录");
  });
  it("isolates malformed files without losing valid credentials", async () => {
    const result = await readCPAFiles([
      {
        name: "valid.json",
        text: async () => '{"type":"codex","access_token":"test","refresh_token":"rt"}',
      },
      { name: "broken.json", text: async () => "{invalid" },
    ]);
    expect(result.invalidFiles).toBe(1);
    expect(result.accounts).toEqual([{ type: "codex", access_token: "test", refresh_token: "rt" }]);
  });
});
