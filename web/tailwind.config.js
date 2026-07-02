/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        ink: "#172026",
        line: "#d8dee4",
        mint: "#1f9d72",
        amber: "#b7791f",
      },
    },
  },
  plugins: [],
};
