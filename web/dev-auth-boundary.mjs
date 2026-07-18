export const developmentOrigin = "http://127.0.0.1:5858";

export function isTrustedDevelopmentApiRequest(headers) {
  const fetchSite = headers["sec-fetch-site"];
  return (
    headers.host === "127.0.0.1:5858" &&
    (!fetchSite || fetchSite === "same-origin" || fetchSite === "none") &&
    (!headers.origin || headers.origin === developmentOrigin)
  );
}
