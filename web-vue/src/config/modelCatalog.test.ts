import { describe, expect, it } from "vite-plus/test";
import type { Settings } from "@/types/api";
import { FALLBACK_CHAT_MODELS, resolveChatModels, resolveImageModels } from "./modelCatalog";

describe("model catalog", () => {
  it("uses the server chat catalog as-is and keeps image models separate", () => {
    const models = ["gpt-5-6-sol", "gpt-5"];
    const settings = { model_catalog: { chat_models: models } } as Settings;
    expect(resolveChatModels(settings)).toEqual(models);
    expect(resolveImageModels(settings)).not.toContain("gpt-5-6-sol");
  });

  it("falls back to web chat models", () => {
    expect(resolveChatModels(null)).toEqual(FALLBACK_CHAT_MODELS);
    expect(resolveChatModels(null)).toContain("gpt-5-6-sol");
  });

  it("includes GPT Image 2.6 in fallback image models", () => {
    expect(resolveImageModels(null)).toContain("gpt-image-2.6");
  });
});
