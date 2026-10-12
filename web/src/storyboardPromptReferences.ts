export interface PromptPreviewPart {
  text: string;
  assetID?: string;
  referenceKey?: string;
  implicitAssetMention?: boolean;
}

export function splitPromptAssetMentions(
  value: string,
  assets: Array<{ asset_id: string; name: string; plain?: boolean }>,
): PromptPreviewPart[] {
  const mentions: Array<{
    mention: string;
    asset_id?: string;
    referenceKey?: string;
    implicitAssetMention: boolean;
  }> = [
    ...assets
      .filter((asset) => asset.name)
      .flatMap((asset) => [
        { asset_id: asset.asset_id, mention: `@${asset.name}`, implicitAssetMention: false },
        ...(asset.plain
          ? [{ asset_id: asset.asset_id, mention: asset.name, implicitAssetMention: true }]
          : []),
      ]),
  ].sort((left, right) => right.mention.length - left.mention.length);
  const parts: PromptPreviewPart[] = [];
  let cursor = 0;

  while (cursor < value.length) {
    let next:
      | {
          mention: string;
          asset_id?: string;
          referenceKey?: string;
          implicitAssetMention: boolean;
        }
      | undefined;
    let nextIndex = value.length;
    for (const mention of mentions) {
      let index = value.indexOf(mention.mention, cursor);
      while (index >= 0 && mention.implicitAssetMention) {
        const before = value[index - 1] ?? '';
        const after = value[index + mention.mention.length] ?? '';
        const startsWithASCIIWord = /^[A-Za-z0-9_]/.test(mention.mention);
        const endsWithASCIIWord = /[A-Za-z0-9_]$/.test(mention.mention);
        if (
          (!startsWithASCIIWord || !/[A-Za-z0-9_]/.test(before)) &&
          (!endsWithASCIIWord || !/[A-Za-z0-9_]/.test(after))
        )
          break;
        index = value.indexOf(mention.mention, index + mention.mention.length);
      }
      if (index >= 0 && index < nextIndex) {
        next = mention;
        nextIndex = index;
      }
    }
    if (!next) {
      parts.push({ text: value.slice(cursor) });
      break;
    }
    if (nextIndex > cursor) parts.push({ text: value.slice(cursor, nextIndex) });
    parts.push({
      text: next.mention,
      ...(next.asset_id ? { assetID: next.asset_id } : {}),
      ...(next.referenceKey ? { referenceKey: next.referenceKey } : {}),
      ...(next.implicitAssetMention ? { implicitAssetMention: true } : {}),
    });
    cursor = nextIndex + next.mention.length;
  }

  return parts.length ? parts : [{ text: value }];
}
