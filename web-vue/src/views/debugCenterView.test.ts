import { describe, expect, it } from "vite-plus/test";
import {
  DEBUG_SEARCH_API_KEY_PLACEHOLDER,
  buildDebugSearchSkillInstallPrompt,
  buildDebugSearchSkillMarkdown,
  debugChatReasoningEffortOptions,
} from "./debugCenterView";
import {
  EDITABLE_REFERENCE_IMAGE_MAX_BYTES,
  EDITABLE_REFERENCE_IMAGE_MAX_COUNT,
  validateEditableReferenceFiles,
} from "./debugCenterEditableImages";

describe("debug center helpers", () => {
  it("keeps Skills copy free of console tokens and documents real sources", () => {
    const markdown = buildDebugSearchSkillMarkdown("https://image-2.elycyrene.net", "zh");
    const install = buildDebugSearchSkillInstallPrompt("https://image-2.elycyrene.net", "en");
    expect(markdown).toContain(DEBUG_SEARCH_API_KEY_PLACEHOLDER);
    expect(markdown).toContain("POST https://image-2.elycyrene.net/v1/search");
    expect(markdown).toContain("`sources`");
    expect(markdown).not.toMatch(/Bearer (?!<YOUR_API_KEY>)\S+/);
    expect(install).toContain(DEBUG_SEARCH_API_KEY_PLACEHOLDER);
    expect(install).not.toContain("localStorage");
    expect(install).not.toMatch(/Bearer (?!<YOUR_API_KEY>)\S+/);
  });

  it("hides invalid web chat thinking effort options", () => {
    for (const model of ["auto", "gpt-5", "gpt-5-5", "gpt-5-6"]) {
      const values = debugChatReasoningEffortOptions(model).map((option) => option.value);
      expect(values).toEqual([""]);
      expect(values).not.toContain("low");
      expect(values).not.toContain("medium");
      expect(values).not.toContain("high");
      expect(values).not.toContain("extended");
    }
  });

  it("validates editable reference image count, type and size without exposing base64", () => {
    expect(validateEditableReferenceFiles(0, [])).toBe("请选择至少一张参考图");
    expect(
      validateEditableReferenceFiles(0, [{ name: "a.txt", size: 10, type: "text/plain" }]),
    ).toContain("不是图片");
    expect(
      validateEditableReferenceFiles(0, [
        { name: "big.png", size: EDITABLE_REFERENCE_IMAGE_MAX_BYTES + 1, type: "image/png" },
      ]),
    ).toContain("16MB");
    expect(
      validateEditableReferenceFiles(EDITABLE_REFERENCE_IMAGE_MAX_COUNT, [
        { name: "extra.png", size: 10, type: "image/png" },
      ]),
    ).toContain("最多选择");
    expect(
      validateEditableReferenceFiles(0, [{ name: "ok.png", size: 10, type: "image/png" }]),
    ).toBe("");
    expect(
      validateEditableReferenceFiles(0, [{ name: "ok.png", size: 10, type: "image/png" }]),
    ).not.toMatch(/base64/i);
  });
});
