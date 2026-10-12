export function truncateUnicode(value: string, maximumCharacters: number) {
  if (maximumCharacters < 1) return '';
  const characters = Array.from(value);
  return characters.length > maximumCharacters ? characters.slice(0, maximumCharacters).join('') : value;
}
