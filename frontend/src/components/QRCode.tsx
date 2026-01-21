import ReactQRCode from "react-qr-code";
import LokiHead from "src/assets/loki.svg?react";
import { cn } from "src/lib/utils";

export type Props = {
  value: string;
  size?: number;
  className?: string;

  // set the level to Q if there are overlays
  // Q will improve error correction (so we can add overlays covering up to 25% of the QR)
  // at the price of decreased information density (meaning the QR codes "pixels" have to be
  // smaller to encode the same information).
  // While that isn't that much of a problem for lightning addresses (because they are usually quite short),
  // for invoices that contain larger amount of data those QR codes can get "harder" to read.
  // (meaning you have to aim your phone very precisely and have to wait longer for the reader
  // to recognize the QR code)
  level?: "L" | "M" | "Q" | "H";
  withIcon?: boolean;
};

function QRCode({ value, size, level, className, withIcon = true }: Props) {
  // Do not use dark mode: some apps do not handle it well (e.g. Phoenix)
  // const { isDarkMode } = useTheme();
  const fgColor = "#242424"; // isDarkMode ? "#FFFFFF" : "#242424";
  const bgColor = "#FFFFFF"; // isDarkMode ? "#242424" : "#FFFFFF";

  // Use Q level by default if there is an icon overlay to ensure better scannability
  const qrLevel = withIcon ? "Q" : (level || "L");

  return (
    <div className={cn("bg-white p-2 rounded-md relative flex items-center justify-center w-fit mx-auto", className)}>
      <ReactQRCode
        value={value}
        size={size}
        fgColor={fgColor}
        bgColor={bgColor}
        className="rounded"
        level={qrLevel}
      />
      {withIcon && (
        <div className="absolute rounded-full p-1 bg-white">
          <LokiHead className="w-12 h-12" />
        </div>
      )}
    </div>
  );
}

export default QRCode;
