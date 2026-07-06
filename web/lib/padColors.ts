// Launchkey palette indexes we bother mapping; everything else is gray.
// Same table as the interim dashboard.
const PAD_COLORS: Record<number, string> = {
  3: "#ffffff",
  5: "#ff3b30",
  9: "#ff9500",
  13: "#ffd60a",
  25: "#34c759",
  33: "#32ade6",
  41: "#0a5cff",
  49: "#af52de",
  57: "#ff2d92",
};

export const padColor = (index: number): string => PAD_COLORS[index] ?? "#8e8e93";
