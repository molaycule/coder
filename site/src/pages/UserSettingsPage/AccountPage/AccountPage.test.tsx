import { fireEvent, screen, waitFor } from "@testing-library/react"
import { Language as ErrorSummaryLanguage } from "components/ErrorSummary/ErrorSummary"
import * as API from "../../../api/api"
import { GlobalSnackbar } from "../../../components/GlobalSnackbar/GlobalSnackbar"
import * as AccountForm from "../../../components/SettingsAccountForm/SettingsAccountForm"
import { renderWithAuth } from "../../../testHelpers/renderHelpers"
import * as AuthXService from "../../../xServices/auth/authXService"
import { AccountPage } from "./AccountPage"

const renderPage = () => {
  return renderWithAuth(
    <>
      <AccountPage />
      <GlobalSnackbar />
    </>,
  )
}

const newData = {
  username: "user",
}

const fillAndSubmitForm = async () => {
  await waitFor(() => screen.findByLabelText("Username"))
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: newData.username } })
  fireEvent.click(screen.getByText(AccountForm.Language.updateSettings))
}

describe("AccountPage", () => {
  describe("when it is a success", () => {
    it("shows the success message", async () => {
      jest.spyOn(API, "updateProfile").mockImplementationOnce((userId, data) =>
        Promise.resolve({
          id: userId,
          email: "user@coder.com",
          created_at: new Date().toString(),
          status: "active",
          organization_ids: ["123"],
          roles: [],
          avatar_url: "",
          ...data,
        }),
      )
      const { user } = renderPage()
      await fillAndSubmitForm()

      const successMessage = await screen.findByText(AuthXService.Language.successProfileUpdate)
      expect(successMessage).toBeDefined()
      expect(API.updateProfile).toBeCalledTimes(1)
      expect(API.updateProfile).toBeCalledWith(user.id, newData)
    })
  })

  describe("when the username is already taken", () => {
    it("shows an error", async () => {
      jest.spyOn(API, "updateProfile").mockRejectedValueOnce({
        isAxiosError: true,
        response: {
          data: {
            message: "Invalid profile",
            validations: [{ detail: "Username is already in use", field: "username" }],
          },
        },
      })

      const { user } = renderPage()
      await fillAndSubmitForm()

      const errorMessage = await screen.findByText("Username is already in use")
      expect(errorMessage).toBeDefined()
      expect(API.updateProfile).toBeCalledTimes(1)
      expect(API.updateProfile).toBeCalledWith(user.id, newData)
    })
  })

  describe("when it is an unknown error", () => {
    it("shows a generic error message", async () => {
      jest.spyOn(API, "updateProfile").mockRejectedValueOnce({
        data: "unknown error",
      })

      const { user } = renderPage()
      await fillAndSubmitForm()

      const errorMessage = await screen.findByText(ErrorSummaryLanguage.unknownErrorMessage)
      expect(errorMessage).toBeDefined()
      expect(API.updateProfile).toBeCalledTimes(1)
      expect(API.updateProfile).toBeCalledWith(user.id, newData)
    })
  })
})
